/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package static

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	so "github.com/trickstercache/trickster/v2/pkg/backends/static/options"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"

	"golang.org/x/sync/singleflight"
)

const (
	allowedMethods = "GET, HEAD, OPTIONS"
	rootName       = "."
)

// ErrMissingOptions is returned when a file server is built without its options
var ErrMissingOptions = errors.New("static backend requires the static options block")

// rootHandle pairs the open content root with the identity it was opened at,
// so a root that is later replaced on disk (e.g., a symlink swap) is detected.
type rootHandle struct {
	root *os.Root
	info os.FileInfo
}

// server is the file server. All file access goes through an os.Root, which
// refuses any path (including via symlinks) that resolves outside the root.
type server struct {
	name         string
	opts         *so.Options
	compressible sets.Set[string]
	root         atomic.Pointer[rootHandle]
	cache        *fileCache
	watcher      *filesystem.DirWatcher
	// loads collapses concurrent loads of one file, or encodings of one rendition, into one
	loads singleflight.Group
	// responses is the backend's response counters, which are private until it is in service
	responses atomic.Pointer[responseSeries]
	// stores counts the cache stores still to be made, which are made off the request path,
	// and started every one that has been
	stores  sync.WaitGroup
	started atomic.Int64
}

func newServer(name string, o *so.Options, compressible sets.Set[string]) (*server, error) {
	if o == nil {
		return nil, ErrMissingOptions
	}
	s := &server{name: name, opts: o, compressible: compressible}
	s.responses.Store(unpublishedResponses())
	if err := s.openRoot(); err != nil {
		return nil, err
	}
	if mc := o.FileserverCache; mc != nil && !mc.Disabled {
		w, err := filesystem.NewDirWatcher(&filesystem.DirOptions{
			Name:       "static:" + name,
			Interval:   time.Duration(mc.RevalidationInterval),
			OnEvent:    s.onFileEvent,
			OnLost:     func() { s.cache.purge() },
			OnInterval: s.revalidate,
		})
		if err != nil {
			return nil, err
		}
		s.watcher = w
		s.cache = newFileCache(name, mc.MaxFileSizeBytes, mc.MaxSizeBytes, mc.MaxFiles, w)
	}
	return s, nil
}

func (s *server) openRoot() error {
	root, err := os.OpenRoot(s.opts.Root)
	if err != nil {
		return err
	}
	info, err := root.Stat(rootName)
	if err != nil {
		return errors.Join(err, root.Close())
	}
	// a replaced handle is not closed here as requests may still be using it;
	// its finalizer releases it once they are done
	s.root.Store(&rootHandle{root: root, info: info})
	return nil
}

// start puts the server in service: its series are published, and its fileserver cache
// begins holding files and watching for changes to them
func (s *server) start() {
	claimSeries(s.name, s)
	s.responses.Store(publishedResponses(s.name))
	if s.watcher == nil {
		return
	}
	s.cache.activate()
	s.watcher.Start()
}

// stop takes the server out of service; requests still in flight are served from disk
func (s *server) stop() {
	releaseSeries(s.name, s)
	if s.watcher == nil {
		return
	}
	s.cache.retire()
	s.watcher.Close()
	s.cache.purge()
}

// counted records a response in the backend's metrics
func (s *server) counted(status cacheStatus, enc providers.Provider) {
	s.responses.Load()[status][enc].Inc()
}

func (s *server) onFileEvent(name string) {
	rel, err := filepath.Rel(s.opts.Root, name)
	if err != nil || rel == rootName || strings.HasPrefix(rel, "..") {
		s.cache.purge()
		return
	}
	// a file costs a lookup; only a directory that holds files has a subtree to drop
	s.cache.invalidatePath(filepath.ToSlash(rel))
}

// revalidate is the backstop for changes that raise no event, such as a
// replaced root, a changed symlink target or a filesystem without events.
func (s *server) revalidate() {
	rh := s.root.Load()
	if fi, err := os.Stat(s.opts.Root); err == nil && !os.SameFile(rh.info, fi) {
		if err = s.openRoot(); err == nil {
			s.cache.purge()
			rh = s.root.Load()
		}
	}
	s.cache.revalidate(func(key string, e *entry) bool {
		fi, err := rh.root.Stat(key)
		return err != nil || !sameFile(e.info, fi)
	})
}

// sameFile reports whether b is the file a described, unchanged: the same
// identity, size and modification time, which are also all the ETag reflects
func sameFile(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

// allowedDotSegments are the names exempt from the refusal of dotfiles, and only
// as a path's first segment. It is deliberately not configurable.
var allowedDotSegments = []string{".well-known"}

func allowedDotSegment(p string, i int) bool {
	if i != 0 {
		return false
	}
	for _, name := range allowedDotSegments {
		rest, ok := strings.CutPrefix(p[1:], name)
		if ok && (rest == "" || rest[0] == '/') {
			return true
		}
	}
	return false
}

// resolve maps a request path to a root-relative name, refusing dotfiles
func resolve(p string) (name string, dirRequest bool, ok bool) {
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	if strings.ContainsAny(p, "\\\x00") {
		return "", false, false
	}
	dirRequest = p[len(p)-1] == '/'
	p = path.Clean(p)
	for i := range len(p) - 1 {
		if p[i] == '/' && p[i+1] == '.' && !allowedDotSegment(p, i) {
			return "", false, false
		}
	}
	if name = p[1:]; name == "" {
		name = rootName
	}
	return name, dirRequest, true
}

func (s *server) defaultFileIn(dir string) string {
	if dir == rootName {
		return s.opts.DefaultFile
	}
	return dir + "/" + s.opts.DefaultFile
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodOptions:
		w.Header().Set(headers.NameAllow, allowedMethods)
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set(headers.NameAllow, allowedMethods)
		writeError(w, http.StatusMethodNotAllowed)
		return
	}
	name, dirRequest, ok := resolve(r.URL.Path)
	if !ok {
		s.notFound(w, r, false)
		return
	}
	key := name
	if dirRequest {
		key = s.defaultFileIn(name)
	}
	s.serveKey(w, r, key, dirRequest, false)
}

// serveKey serves the file at key. fallback is true when key is the not-found
// file, which is answered with a plain 404 if it is itself absent.
func (s *server) serveKey(w http.ResponseWriter, r *http.Request, key string, dirRequest, fallback bool) {
	accepted := acceptedEncodings(r)
	// of the renditions that are held, the one the client most prefers
	for i := range accepted.Len() {
		if v := s.cache.get(key, accepted.At(i)); v != nil {
			s.serveRendition(w, r, v)
			return
		}
	}
	if e := s.cache.get(key, providers.Identity); e != nil {
		s.serveEntry(w, r, e, nil, accepted, statusHit)
		return
	}
	// nothing of the file is held, so it is read from disk: a held rendition in an
	// encoding the client didn't ask for is never decoded to stand in for the file
	s.serveFromDisk(w, r, key, dirRequest, fallback, accepted)
}

// serveEntry serves a file from memory, as a new rendition where the request is suited to
// one. pending is the file's own store where that is still to be made, and is nil otherwise.
func (s *server) serveEntry(w http.ResponseWriter, r *http.Request, e *entry, pending *reservation,
	accepted providers.Accepted, status cacheStatus,
) {
	// an encoding found not to shrink this file is passed over for the next the client accepts
	unhelpful := e.unhelpful()
	if usable := accepted.Filter(^unhelpful); s.wantsRendition(r, usable, e.compressible) {
		if status == statusHit {
			status = statusPartialHit
		}
		s.streamRendition(w, r, e, pending, usable.Preferred(), status)
		return
	}
	s.storeEntry(e, pending)
	withoutEncodings(r, unhelpful)
	s.counted(status, providers.Identity)
	s.serve(w, r, &e.fileMeta, bytes.NewReader(e.body))
}

// withoutEncodings keeps the response path from using encodings that are no use to a file,
// which it would otherwise do again on every request
func withoutEncodings(r *http.Request, unhelpful providers.Provider) {
	if unhelpful == 0 {
		return
	}
	if ep := profile.FromContext(r.Context()); ep != nil {
		ep.Supported &^= unhelpful
	}
}

// wantsRendition reports whether a response is worth encoding and holding: one that
// sends the whole file. A conditional request may need no body, so it is left alone.
func (s *server) wantsRendition(r *http.Request, accepted providers.Accepted, compressible bool) bool {
	if accepted.Len() == 0 || !compressible || r.Method != http.MethodGet {
		return false
	}
	return !conditional(r)
}

// conditional reports whether a request carries a precondition, and so may be answered
// without the body of what it asked for
func conditional(r *http.Request) bool {
	h := r.Header
	return h.Get(headers.NameIfNoneMatch) != "" || h.Get(headers.NameIfModifiedSince) != "" ||
		h.Get(headers.NameIfMatch) != "" || h.Get(headers.NameIfUnmodifiedSince) != ""
}

// renditionHeaders describes an encoded response, and tells the response path that it
// is already encoded so that it is passed through as it is.
func renditionHeaders(w http.ResponseWriter, r *http.Request, m *fileMeta, enc providers.Provider) {
	name := enc.String()
	if ep := profile.FromContext(r.Context()); ep != nil {
		ep.ContentEncoding = name
	}
	h := w.Header()
	h.Set(headers.NameContentType, m.contentType)
	if m.cacheControl != "" {
		h.Set(headers.NameCacheControl, m.cacheControl)
	}
	h.Add(headers.NameVary, headers.NameAcceptEncoding)
	h.Set(headers.NameContentEncoding, name)
	// weak, as the validator describes the file as stored rather than these bytes
	h.Set(headers.NameETag, m.weakETag)
}

// serveRendition writes a held rendition
func (s *server) serveRendition(w http.ResponseWriter, r *http.Request, v *entry) {
	s.counted(statusHit, v.encoding)
	renditionHeaders(w, r, &v.fileMeta, v.encoding)
	// ServeContent leaves the length off an encoded response, which would then be sent in
	// chunks. It is known here, and is given unless a precondition may leave the body out.
	if !conditional(r) {
		w.Header().Set(headers.NameContentLength, strconv.Itoa(len(v.body)))
	}
	http.ServeContent(w, r, "", v.modTime, bytes.NewReader(v.body))
}

// streamRendition encodes a file straight to the response, keeping a copy of what it
// sends to hold as the rendition. Another request already doing so for the same rendition,
// or a cache without the room, leaves this one to encode for its own response alone.
func (s *server) streamRendition(w http.ResponseWriter, r *http.Request, e *entry, pending *reservation,
	enc providers.Provider, status cacheStatus,
) {
	s.counted(status, enc)
	// reserved at the size of the file, which a rendition worth holding is smaller than
	rsv := s.cache.reserve(e.key, enc, s.osDir(e.key), int64(len(e.body)), e)
	// only now is the file's own store let go, as it would otherwise be what the rendition's
	// reservation found the cache busy with; it isn't left until the response is sent, either
	s.storeEntry(e, pending)
	renditionHeaders(w, r, &e.fileMeta, enc)
	if !e.modTime.IsZero() {
		w.Header().Set(headers.NameLastModified, e.modTime.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
	var dst io.Writer = w
	var tee *teeBuffer
	if rsv != nil {
		tee = &teeBuffer{limit: len(e.body) - 1}
		dst = io.MultiWriter(w, tee)
	}
	ew := newEncoder(enc, dst)
	_, err := ew.Write(e.body)
	if cerr := ew.Close(); err == nil {
		err = cerr
	}
	if rsv == nil {
		return
	}
	if err == nil && tee.overflow {
		// this encoding is no use to the file; the others are still to be found out
		e.unencodable.Or(uint32(enc))
	}
	// the response is complete; what is left is bookkeeping, which it doesn't wait on
	s.storeAsync(func() {
		if err != nil || tee.overflow {
			rsv.cancel()
			return
		}
		// the file's own store comes first, as a rendition is only held alongside its file;
		// it is usually made by now, and is made here if not
		if pending != nil {
			pending.commit(e)
		}
		// copied to size, as the buffer grew by doubling and would hold the excess too
		body := make([]byte, len(tee.buf))
		copy(body, tee.buf)
		rsv.commit(&entry{fileMeta: e.fileMeta, body: body, key: e.key, encoding: enc})
	})
}

// storeAsync runs a cache store away from the request that produced it, so that the
// response is never held up behind the cache's lock
func (s *server) storeAsync(store func()) {
	s.stores.Add(1)
	s.started.Add(1)
	safego.Go(func(r any, stack []byte) {
		logger.Error("static file cache store panic", logging.Pairs{
			keys.BackendName: s.name, "panic": r, "stack": string(stack),
		})
	}, func() {
		defer s.stores.Done()
		store()
	})
}

func (s *server) osDir(key string) string {
	return filepath.Join(s.opts.Root, filepath.FromSlash(path.Dir(key)))
}

// notFound answers a request for nothing that can be served, with the configured
// not-found file where there is one, at its configured status.
func (s *server) notFound(w http.ResponseWriter, r *http.Request, fallback bool) {
	key := s.opts.NotFoundFile
	if key == "" || fallback || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		writeError(w, http.StatusNotFound)
		return
	}
	if s.opts.NotFoundStatus == http.StatusOK {
		// served as the page it is, since the application at it handles the request's path
		s.serveKey(w, r, key, false, true)
		return
	}
	// an error page is the same for every request, whatever the request asked of the missing file
	r = r.Clone(r.Context())
	for _, name := range []string{
		headers.NameRange, headers.NameIfRange, headers.NameIfNoneMatch, headers.NameIfModifiedSince,
		headers.NameIfMatch, headers.NameIfUnmodifiedSince,
	} {
		r.Header.Del(name)
	}
	s.serveKey(&errorPageWriter{ResponseWriter: w}, r, key, false, true)
}

// errorPageWriter sends a file as the body of a 404, without the headers that
// would let it be revalidated, ranged over or reused as though it were the file.
type errorPageWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (e *errorPageWriter) WriteHeader(code int) {
	if !e.wroteHeader {
		e.wroteHeader = true
		if code == http.StatusOK {
			code = http.StatusNotFound
			h := e.Header()
			h.Del(headers.NameETag)
			h.Del(headers.NameLastModified)
			h.Del(headers.NameAcceptRanges)
			h.Set(headers.NameCacheControl, headers.ValueNoCache)
		}
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *errorPageWriter) Write(b []byte) (int, error) {
	if !e.wroteHeader {
		e.WriteHeader(http.StatusOK)
	}
	return e.ResponseWriter.Write(b)
}

func (e *errorPageWriter) Unwrap() http.ResponseWriter {
	return e.ResponseWriter
}

func (s *server) serveFromDisk(w http.ResponseWriter, r *http.Request, key string, dirRequest, fallback bool,
	accepted providers.Accepted,
) {
	root := s.root.Load().root
	// checked before opening: opening a pipe would block until it had a writer
	fi, err := root.Stat(key)
	if err != nil {
		s.fail(w, r, key, err, fallback)
		return
	}
	if fi.IsDir() && !dirRequest && !fallback {
		redirectToDir(w, r)
		return
	}
	// a directory, device, pipe or socket is never content
	if !fi.Mode().IsRegular() {
		s.notFound(w, r, fallback)
		return
	}
	f, err := root.Open(key)
	if err != nil {
		s.fail(w, r, key, err, fallback)
		return
	}
	defer f.Close()
	// the open file is the authority from here on, as the path may have been replaced
	if fi, err = f.Stat(); err != nil || !fi.Mode().IsRegular() {
		s.notFound(w, r, fallback)
		return
	}
	ct := typeByExtension(key, s.opts.MIMETypes)
	if ct == "" {
		head := make([]byte, sniffLen)
		n, _ := io.ReadFull(f, head)
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			s.fail(w, r, key, err, fallback)
			return
		}
		ct = sniffType(head[:n])
	}
	m := s.newMeta(key, fi, ct)
	// a file sent in part is not worth holding, and a large one is sent as it is
	if r.Header.Get(headers.NameRange) != "" || !s.cache.admits(key, fi.Size()) {
		s.counted(statusDisk, providers.Identity)
		s.serve(w, r, &m, f)
		return
	}
	// a rendition is made from the whole file, so a response suited to one loads it now
	if m.size >= minRenditionSize && s.wantsRendition(r, accepted, m.compressible) {
		if e, pending := s.load(root, f, &m, key); e != nil {
			s.serveEntry(w, r, e, pending, accepted, statusMiss)
			return
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			s.fail(w, r, key, err, fallback)
			return
		}
	}
	if m.size < minRenditionSize {
		withoutEncodings(r, providers.AllSupportedWebProvidersBitmap)
	}
	l := &lazyContent{s: s, root: root, f: f, meta: &m, key: key}
	s.serve(w, r, &m, l)
	if l.body != nil {
		s.counted(statusMiss, providers.Identity)
		return
	}
	s.counted(statusDisk, providers.Identity)
}

// lazyContent defers reading a file until the response is known to need its
// body, which a HEAD request or an unmodified conditional one never does.
type lazyContent struct {
	s    *server
	root *os.Root
	f    *os.File
	meta *fileMeta
	key  string

	off      int64
	body     []byte
	resolved bool
}

func (l *lazyContent) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += l.off
	case io.SeekEnd:
		offset += l.meta.size
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	l.off = offset
	if l.resolved && l.body == nil {
		return l.f.Seek(offset, io.SeekStart)
	}
	return offset, nil
}

func (l *lazyContent) Read(p []byte) (int, error) {
	if !l.resolved {
		l.resolved = true
		if e, pending := l.s.load(l.root, l.f, l.meta, l.key); e != nil {
			l.s.storeEntry(e, pending)
			l.body = e.body
		} else if _, err := l.f.Seek(l.off, io.SeekStart); err != nil {
			return 0, err
		}
	}
	if l.body == nil {
		n, err := l.f.Read(p)
		l.off += int64(n)
		return n, err
	}
	if l.off >= int64(len(l.body)) {
		return 0, io.EOF
	}
	n := copy(p, l.body[l.off:])
	l.off += int64(n)
	return n, nil
}

// loaded is the result of a load: the file's entry, and its store where still to be made
type loaded struct {
	entry   *entry
	pending *reservation
}

// load returns the file's entry, read from disk if it isn't held, and the entry's store if still to
// be made. Concurrent loads share one read. It returns nil when the file can't be held.
func (s *server) load(root *os.Root, f *os.File, m *fileMeta, key string) (*entry, *reservation) {
	v, _, _ := s.loads.Do(key, func() (any, error) {
		if e := s.cache.get(key, providers.Identity); e != nil {
			return loaded{entry: e}, nil
		}
		return s.read(root, f, m, key), nil
	})
	// the response headers describe this request's file, which a shared or
	// previously held entry matches only if nothing changed in between
	if l := v.(loaded); l.entry != nil && sameFile(l.entry.info, m.info) {
		return l.entry, l.pending
	}
	return nil, nil
}

func (s *server) read(root *os.Root, f *os.File, m *fileMeta, key string) loaded {
	// capacity is reserved before the body is allocated, and the directory is
	// watched before the file is read so that any later change raises an event
	rsv := s.cache.reserve(key, providers.Identity, s.osDir(key), m.size, nil)
	if rsv == nil {
		return loaded{}
	}
	// the opened file may have been replaced on disk before the watch was armed
	if cur, err := root.Stat(key); err != nil || !sameFile(m.info, cur) {
		s.storeAsync(rsv.cancel)
		return loaded{}
	}
	body := make([]byte, m.size)
	if _, err := io.ReadFull(f, body); err != nil {
		s.storeAsync(rsv.cancel)
		return loaded{}
	}
	if cur, err := f.Stat(); err != nil || !sameFile(m.info, cur) {
		s.storeAsync(rsv.cancel)
		return loaded{}
	}
	e := &entry{fileMeta: *m, body: body, key: key}
	if m.size < minRenditionSize {
		e.unencodable.Store(uint32(providers.AllSupportedWebProvidersBitmap))
	}
	// the caller decides when the store is made; the entry serves its request either way
	return loaded{entry: e, pending: rsv}
}

// storeEntry makes a loaded file's store, away from the request. Loads that shared one read
// each ask for it, and it is made, and started, only once.
func (s *server) storeEntry(e *entry, pending *reservation) {
	// claimed before anything is started, so that a burst of loads that shared one read
	// starts one store between them, rather than one each to find the store already made
	if pending != nil && pending.claim() {
		s.storeAsync(func() { pending.commit(e) })
	}
}

func (s *server) newMeta(key string, fi os.FileInfo, ct string) fileMeta {
	m := fileMeta{
		info:        fi,
		modTime:     fi.ModTime(),
		size:        fi.Size(),
		contentType: ct,
	}
	// the modification time and size, as other web servers use. Derived from metadata
	// alone, it is the same from memory or disk and validates without reading the file.
	tag := strconv.FormatInt(m.modTime.UnixNano(), 16) + "-" + strconv.FormatInt(m.size, 16)
	m.etag = `"` + tag + `"`
	m.weakETag = `W/"` + tag + `"`
	m.compressible = s.compressible.Contains(baseType(m.contentType))
	m.cacheControl = s.opts.CacheControl
	if cc, ok := s.opts.CacheControlByExtension[strings.ToLower(path.Ext(key))]; ok {
		m.cacheControl = cc
	}
	return m
}

// serve writes the file, leaving conditional and range handling to http.ServeContent
func (s *server) serve(w http.ResponseWriter, r *http.Request, m *fileMeta, content io.ReadSeeker) {
	h := w.Header()
	h.Set(headers.NameContentType, m.contentType)
	if m.cacheControl != "" {
		h.Set(headers.NameCacheControl, m.cacheControl)
	}
	etag := m.etag
	if m.compressible {
		h.Add(headers.NameVary, headers.NameAcceptEncoding)
		// the response will be encoded on its way out, so the validator is weak
		if ep := profile.FromContext(r.Context()); ep != nil && ep.Supported != 0 {
			etag = m.weakETag
			w = &codingGuard{ResponseWriter: w, profile: ep, etag: m.etag}
		}
	}
	h.Set(headers.NameETag, etag)
	http.ServeContent(w, r, "", m.modTime, content)
}

// codingGuard keeps anything but a complete 200 response from being encoded
// downstream, as byte ranges and validators describe the file as stored.
type codingGuard struct {
	http.ResponseWriter
	profile     *profile.Profile
	etag        string
	wroteHeader bool
}

func (g *codingGuard) WriteHeader(code int) {
	if !g.wroteHeader {
		g.wroteHeader = true
		if code != http.StatusOK {
			g.profile.Supported = 0
		}
		if code == http.StatusPartialContent {
			g.Header().Set(headers.NameETag, g.etag)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *codingGuard) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	return g.ResponseWriter.Write(b)
}

func (g *codingGuard) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

// redirectToDir sends a directory requested without its trailing slash to the
// slashed form. The target is relative, so it holds under any routing prefix.
func redirectToDir(w http.ResponseWriter, r *http.Request) {
	u := url.URL{Path: path.Base(r.URL.Path) + "/", RawQuery: r.URL.RawQuery}
	w.Header().Set(headers.NameLocation, u.String())
	w.WriteHeader(http.StatusMovedPermanently)
}

// fail answers a file that can't be opened. Absent, unreadable and
// out-of-root files all look the same to the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, key string, err error, fallback bool) {
	if !errors.Is(err, os.ErrNotExist) {
		logger.Debug("static file unavailable", logging.Pairs{
			keys.BackendName: s.name, keys.Path: key, keys.Detail: err.Error(),
		})
	}
	s.notFound(w, r, fallback)
}

func writeError(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}
