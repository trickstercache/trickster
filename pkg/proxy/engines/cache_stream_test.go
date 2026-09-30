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

package engines

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	encodings "github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/stretchr/testify/require"
)

const (
	streamETag = `"v1"`
	// a content type that is not compressed in the cache, as that of most large objects is not
	streamContentType = "video/mp4"
)

// distinct bytes throughout, so that a range of the object is known by its content
func streamBody() string {
	var sb strings.Builder
	for i := range 4096 {
		sb.WriteString("segment-")
		sb.WriteString(string(rune('a' + i%26)))
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString(";")
	}
	return sb.String()
}

// counts the documents read from the cache it wraps, whole and in parts
type countingCache struct {
	cache.Cache
	mu                sync.Mutex
	opened, retrieved int
	open              []cache.Body
	// whole makes the cache one that reads documents whole, whatever the cache it wraps can do
	whole bool
}

func (c *countingCache) splitter() cache.SplitClient  { return c.Cache.(cache.SplitClient) }
func (c *countingCache) streamer() cache.StreamClient { return c.Cache.(cache.StreamClient) }
func (c *countingCache) SupportsSplit() bool          { return c.splitter().SupportsSplit() }
func (c *countingCache) SupportsStream() bool         { return !c.whole && c.streamer().SupportsStream() }

func (c *countingCache) StoreSplit(key string, meta, body []byte, ttl time.Duration) error {
	return c.splitter().StoreSplit(key, meta, body, ttl)
}

func (c *countingCache) RetrieveSplit(key string) ([]byte, []byte, status.LookupStatus, error) {
	c.mu.Lock()
	c.retrieved++
	c.mu.Unlock()
	return c.splitter().RetrieveSplit(key)
}

// notes how it is read, and when it is closed
type trackedBody struct {
	cache.Body
	closed              bool
	reads, partial, all int
}

func (b *trackedBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Body.Read(p)
}

func (b *trackedBody) ReadAt(p []byte, off int64) (int, error) {
	b.partial++
	return b.Body.ReadAt(p, off)
}

func (b *trackedBody) ReadAll() ([]byte, error) {
	b.all++
	return b.Body.ReadAll()
}

func (b *trackedBody) Close() error {
	b.closed = true
	return b.Body.Close()
}

func (c *countingCache) OpenSplit(key string) ([]byte, cache.Body, status.LookupStatus, error) {
	meta, body, s, err := c.streamer().OpenSplit(key)
	if body != nil {
		tb := &trackedBody{Body: body}
		c.mu.Lock()
		c.opened++
		c.open = append(c.open, tb)
		c.mu.Unlock()
		body = tb
	}
	return meta, body, s, err
}

func (c *countingCache) last() *trackedBody {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open[len(c.open)-1].(*trackedBody)
}

func (c *countingCache) requireAllClosed(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range c.open {
		require.True(t, b.(*trackedBody).closed, "body %d of %d was left open", i+1, len(c.open))
	}
}

type streamHarness struct {
	t     *testing.T
	r     *http.Request
	rsc   *request.Resources
	cache *countingCache
}

func newStreamHarness(t *testing.T, provider, cacheControl string, whole bool) *streamHarness {
	t.Helper()
	ts, _, r, rsc, err := setupTestHarnessOPC("", streamBody(), http.StatusOK, map[string]string{
		headers.NameCacheControl: cacheControl,
		headers.NameETag:         streamETag,
		headers.NameContentType:  streamContentType,
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeTestHarness(ts, r) })
	h := &streamHarness{t: t, r: r, rsc: rsc, cache: &countingCache{Cache: newDiskCache(t, provider), whole: whole}}
	rsc.CacheClient = h.cache
	rsc.CacheConfig = h.cache.Configuration()
	return h
}

type streamResponse struct {
	code   int
	body   string
	header http.Header
}

// the response, less what differs from one response to the next by design
func (h *streamHarness) do(method string, hdrs map[string]string) streamResponse {
	h.t.Helper()
	r := h.r.Clone(h.r.Context())
	r.Method = method
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	ObjectProxyCacheRequest(w, r)
	resp := w.Result()
	b, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	out := streamResponse{code: resp.StatusCode, body: string(b), header: resp.Header.Clone()}
	// the boundary between the parts of a multipart response is made anew for each
	if _, params, err := mime.ParseMediaType(out.header.Get(headers.NameContentType)); err == nil && params["boundary"] != "" {
		out.body = strings.ReplaceAll(out.body, params["boundary"], "BOUNDARY")
		out.header.Set(headers.NameContentType, "multipart/byteranges; boundary=BOUNDARY")
	}
	for _, name := range []string{headers.NameDate, headers.NameAge, headers.NameTricksterResult, "Cache-Status"} {
		out.header.Del(name)
	}
	return out
}

func (h *streamHarness) status(method string, hdrs map[string]string) string {
	h.t.Helper()
	r := h.r.Clone(h.r.Context())
	r.Method = method
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	ObjectProxyCacheRequest(w, r)
	return w.Result().Header.Get(headers.NameTricksterResult)
}

var streamScenarios = []struct {
	name, method string
	headers      map[string]string
	// code is the status the response must have, when it is not zero
	code int
	// streamed: the body is read from the cache as it is written; ranged: how many ranges
	// of it are read, and no more; unread: none of it is

	streamed, unread bool
	ranged           int
}{
	{name: "whole object", method: http.MethodGet, code: http.StatusOK, streamed: true},
	{
		name: "range", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=100-4195"},
		code: http.StatusPartialContent, ranged: 1,
	},
	{
		name: "first byte", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=0-0"},
		code: http.StatusPartialContent, ranged: 1,
	},
	// how these two are answered is the engine's to decide, and is only required to be the same
	{name: "suffix range", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=-500"}},
	{name: "open range", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=40000-"}},
	{
		name: "range past the end", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=44000-99999999"},
		code: http.StatusPartialContent, ranged: 1,
	},
	{
		name: "ranges", method: http.MethodGet, headers: map[string]string{headers.NameRange: "bytes=0-99,2000-2999,4000-4099"},
		code: http.StatusPartialContent, ranged: 3,
	},
	{
		name: "range that cannot be satisfied", method: http.MethodGet,
		headers: map[string]string{headers.NameRange: "bytes=99999999-"}, code: http.StatusRequestedRangeNotSatisfiable, unread: true,
	},
	{
		name: "not modified", method: http.MethodGet, headers: map[string]string{headers.NameIfNoneMatch: streamETag},
		code: http.StatusNotModified, unread: true,
	},
	{
		name: "modified", method: http.MethodGet, headers: map[string]string{headers.NameIfNoneMatch: `"v0"`},
		code: http.StatusOK, streamed: true,
	},
	{
		name: "range of the same object", method: http.MethodGet,
		headers: map[string]string{headers.NameRange: "bytes=100-199", headers.NameIfRange: streamETag},
		code:    http.StatusPartialContent, ranged: 1,
	},
	{
		name: "range of another object", method: http.MethodGet,
		headers: map[string]string{headers.NameRange: "bytes=100-199", headers.NameIfRange: `"v0"`},
		code:    http.StatusOK, streamed: true,
	},
	{name: "head", method: http.MethodHead, code: http.StatusOK},
}

// what a cache that reads bodies in parts serves is, to the byte, what a cache that reads
// them whole serves
func TestStreamedResponsesMatchBuffered(t *testing.T) {
	const fresh = "max-age=60"
	body := streamBody()
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		t.Run(provider, func(t *testing.T) {
			streaming := newStreamHarness(t, provider, fresh, false)
			buffering := newStreamHarness(t, provider, fresh, true)
			require.True(t, streaming.cache.SupportsStream())
			require.False(t, buffering.cache.SupportsStream())
			for _, h := range []*streamHarness{streaming, buffering} {
				miss := h.do(http.MethodGet, nil)
				require.Equal(t, http.StatusOK, miss.code)
				require.Equal(t, body, miss.body)
			}

			for _, test := range streamScenarios {
				t.Run(test.name, func(t *testing.T) {
					streaming.t, buffering.t = t, t
					opened, retrieved := streaming.cache.opened, streaming.cache.retrieved
					got, want := streaming.do(test.method, test.headers), buffering.do(test.method, test.headers)
					require.Equal(t, want.code, got.code)
					require.Equal(t, want.header, got.header)
					require.Equal(t, want.body, got.body)
					streaming.cache.requireAllClosed(t)
					require.Zero(t, buffering.cache.opened)
					if test.code == 0 {
						return
					}
					require.Equal(t, test.code, got.code)
					if test.code == http.StatusOK && test.method == http.MethodGet {
						require.Equal(t, body, got.body)
					}
					if !test.streamed && !test.unread && test.ranged == 0 {
						require.Equal(t, opened, streaming.cache.opened)
						require.Greater(t, streaming.cache.retrieved, retrieved)
						return
					}
					require.Greater(t, streaming.cache.opened, opened, "the body was left in the cache")
					require.Equal(t, retrieved, streaming.cache.retrieved)
					read := streaming.cache.last()
					require.Zero(t, read.all, "the body was not read whole")
					require.Equal(t, test.ranged, read.partial, "ranges read")
					require.Equal(t, test.streamed, read.reads > 0, "read as it was written")
				})
			}
		})
	}
}

func TestStreamedResponseStatus(t *testing.T) {
	h := newStreamHarness(t, providers.Filesystem, "max-age=60", false)
	require.Contains(t, h.status(http.MethodGet, nil), "status=kmiss")
	require.Contains(t, h.status(http.MethodGet, nil), "status=hit")
	require.Contains(t, h.status(http.MethodGet, map[string]string{headers.NameRange: "bytes=5-10"}), "status=hit")
}

// an object that is no longer fresh is read whole, and revalidated as it would be from any cache
func TestStaleObjectIsNotStreamed(t *testing.T) {
	streaming := newStreamHarness(t, providers.Filesystem, "max-age=1", false)
	buffering := newStreamHarness(t, providers.Filesystem, "max-age=1", true)
	for _, h := range []*streamHarness{streaming, buffering} {
		require.Equal(t, http.StatusOK, h.do(http.MethodGet, nil).code)
	}
	time.Sleep(1100 * time.Millisecond)
	got, want := streaming.do(http.MethodGet, nil), buffering.do(http.MethodGet, nil)
	require.Equal(t, want, got)
	require.Equal(t, streamBody(), got.body)
	streaming.cache.requireAllClosed(t)
}

// a small object that was compressed in the cache is read whole, as it must be to be decompressed
func TestCompressedObjectIsNotStreamed(t *testing.T) {
	ts, _, r, rsc, err := setupTestHarnessOPC("", streamBody(), http.StatusOK, map[string]string{
		headers.NameCacheControl: "max-age=60", headers.NameContentType: "application/json",
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeTestHarness(ts, r) })
	c := &countingCache{Cache: newDiskCache(t, providers.Filesystem)}
	rsc.CacheClient, rsc.CacheConfig = c, c.Configuration()
	h := &streamHarness{t: t, r: r, rsc: rsc, cache: c}

	require.Equal(t, streamBody(), h.do(http.MethodGet, nil).body)
	hit := h.do(http.MethodGet, nil)
	require.Equal(t, streamBody(), hit.body)
	part := h.do(http.MethodGet, map[string]string{headers.NameRange: "bytes=100-199"})
	require.Equal(t, http.StatusPartialContent, part.code)
	require.Equal(t, streamBody()[100:200], part.body)
	c.requireAllClosed(t)
}

// concurrent requests for an object that is streamed each read the cache, as the body
// that one of them is sent is kept for none of the others
func TestStreamedResponsesConcurrently(t *testing.T) {
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		t.Run(provider, func(t *testing.T) {
			h := newStreamHarness(t, provider, "max-age=60", false)
			body := streamBody()
			require.Equal(t, body, h.do(http.MethodGet, nil).body)
			var wg sync.WaitGroup
			for range 32 {
				wg.Go(func() {
					r := h.r.Clone(h.r.Context())
					w := httptest.NewRecorder()
					ObjectProxyCacheRequest(w, r)
					if got := w.Body.String(); got != body || w.Code != http.StatusOK {
						t.Errorf("status %d, and a body of %d bytes", w.Code, len(got))
					}
				})
			}
			wg.Wait()
			h.cache.requireAllClosed(t)
		})
	}
}

// an object that is written again while a response reads it from a cache that opens it
// anew for each read ends the response short, and is not served as a mix of the two
func TestStreamedObjectChangedMidResponse(t *testing.T) {
	c := newDiskCache(t, providers.BBolt)
	sc, _ := streamCache(c)
	d := testDocument([]byte(streamBody()))
	d.Ranges = nil
	require.NoError(t, writeConcurrent(t.Context(), c, "k", d, false, time.Minute))
	qr := queryDeferred(sc, "k", encodings.Identity)
	require.NoError(t, qr.err)
	require.NotNil(t, qr.d.deferred)
	part := make([]byte, 100)
	_, err := qr.d.deferred.Read(part)
	require.NoError(t, err)

	time.Sleep(time.Millisecond)
	require.NoError(t, writeConcurrent(t.Context(), c, "k", d, false, time.Minute))
	_, err = qr.d.deferred.Read(part)
	require.Error(t, err)
	require.Error(t, qr.d.materialize())
}

// a client that goes away once it has been sent a part of the response
type abortingWriter struct {
	header http.Header
	n      int
}

func (w *abortingWriter) Header() http.Header { return w.header }
func (w *abortingWriter) WriteHeader(int)     {}

func (w *abortingWriter) Write(p []byte) (int, error) {
	if w.n += len(p); w.n > 1024 {
		return 0, errTest
	}
	return len(p), nil
}

// whatever ends a response early, what the cache held open for it is let go
func TestStreamedResponseToAClientThatLeaves(t *testing.T) {
	h := newStreamHarness(t, providers.Filesystem, "max-age=60", false)
	require.Equal(t, streamBody(), h.do(http.MethodGet, nil).body)
	opened := h.cache.opened

	func() {
		// a response that cannot be completed is abandoned by a panic the server recovers from
		defer func() { recover() }()
		ObjectProxyCacheRequest(&abortingWriter{header: http.Header{}}, h.r.Clone(h.r.Context()))
	}()
	require.Greater(t, h.cache.opened, opened)
	require.Positive(t, h.cache.last().reads)
	h.cache.requireAllClosed(t)

	// the object is there for the next client, whole
	require.Equal(t, streamBody(), h.do(http.MethodGet, nil).body)
}

type failingBody struct {
	cache.Body
}

func (failingBody) Read([]byte) (int, error)          { return 0, errTest }
func (failingBody) ReadAt([]byte, int64) (int, error) { return 0, errTest }
func (failingBody) ReadAll() ([]byte, error)          { return nil, errTest }

func TestDeferredBody(t *testing.T) {
	c := newDiskCache(t, providers.Filesystem)
	sc, ok := streamCache(c)
	require.True(t, ok)
	body := []byte(streamBody())
	d := testDocument(body)
	d.Ranges = nil
	require.NoError(t, writeConcurrent(t.Context(), c, "k", d, false, time.Minute))

	qr := queryDeferred(sc, "k", encodings.Identity)
	require.NoError(t, qr.err)
	require.Nil(t, qr.d.Body)
	require.NotNil(t, qr.d.deferred)
	require.Same(t, qr.d.deferred, qr.d.ShallowCopy().deferred)
	require.NoError(t, qr.d.materialize())
	require.True(t, bytes.Equal(body, qr.d.Body))
	require.Nil(t, qr.d.deferred)
	require.NoError(t, qr.d.materialize(), "a body that is read already")
	qr.d.releaseBody()
	var none *HTTPDocument
	require.NoError(t, none.materialize())
	none.releaseBody()

	qr = queryDeferred(sc, "absent", encodings.Identity)
	require.ErrorIs(t, qr.err, cache.ErrKNF)

	qr = queryDeferred(sc, "k", encodings.Identity)
	qr.d.deferred = failingBody{qr.d.deferred}
	_, err := qr.d.readRanges(d.getByteRanges())
	require.ErrorIs(t, err, errTest)
	require.ErrorIs(t, qr.d.materialize(), errTest)
	require.Nil(t, qr.d.deferred)
	require.Nil(t, qr.d.Body)
}

// documents whose bodies cannot be left in the cache are read whole, and are what they
// would be from a cache that reads none in parts
func TestQueryDeferredReadsWhole(t *testing.T) {
	c := newDiskCache(t, providers.Filesystem)
	sc, _ := streamCache(c)
	body := []byte(streamBody())
	partial := testDocument(body)
	index := &HTTPDocument{VaryNames: []string{"Accept"}, VaryGeneration: "g"}
	chunk := testDocument(body)
	chunk.Ranges, chunk.IsChunk = nil, true
	empty := testDocument(nil)
	empty.Ranges = nil
	wrongLength := testDocument(body)
	wrongLength.Ranges, wrongLength.ContentLength = nil, 1
	whole := testDocument(body)
	whole.Ranges = nil
	tests := []struct {
		name     string
		d        *HTTPDocument
		compress bool
	}{
		{"part of an object", partial, false}, {"index of variants", index, false}, {"chunk", chunk, false},
		{"no body", empty, false}, {"length not the body's", wrongLength, false}, {"compressed", whole, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, writeConcurrent(t.Context(), c, test.name, test.d, test.compress, time.Minute))
			qr := queryDeferred(sc, test.name, encodings.Identity)
			require.NoError(t, qr.err)
			require.Nil(t, qr.d.deferred)
			want := queryConcurrent(t.Context(), c, test.name)
			require.NoError(t, want.err)
			requireSameDocument(t, want.d, qr.d)
			require.Equal(t, want.d.VaryNames, qr.d.VaryNames)
			require.Equal(t, want.d.IsChunk, qr.d.IsChunk)
		})
	}
	t.Run("stored whole", func(t *testing.T) {
		b, err := whole.MarshalMsg([]byte{0})
		require.NoError(t, err)
		require.NoError(t, c.Store("stored whole", b, time.Minute))
		qr := queryDeferred(sc, "stored whole", encodings.Identity)
		require.NoError(t, qr.err)
		require.Nil(t, qr.d.deferred)
		requireSameDocument(t, whole, qr.d)
	})
	t.Run("meta that cannot be decoded", func(t *testing.T) {
		for name, meta := range map[string][]byte{"flag alone": {0}, "no length": {0, 0x80}, "no document": {0, 1, 0xc1}} {
			require.NoError(t, c.(cache.SplitClient).StoreSplit(name, meta, []byte("b"), time.Minute))
			require.Error(t, queryDeferred(sc, name, encodings.Identity).err, name)
		}
	})
}

// a response writer that keeps nothing of what it is sent
type discardWriter struct {
	header http.Header
	n      int64
}

func (w *discardWriter) Header() http.Header { return w.header }
func (w *discardWriter) WriteHeader(int)     {}

func (w *discardWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func BenchmarkCacheHit(b *testing.B) {
	const size = 64 << 20
	body := strings.Repeat(streamBody(), size/len(streamBody())+1)[:size]
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		ts, _, r, rsc, err := setupTestHarnessOPC("", body, http.StatusOK, map[string]string{
			headers.NameCacheControl: "max-age=600", headers.NameContentType: streamContentType,
		})
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { closeTestHarness(ts, r) })
		c := newDiskCache(b, provider)
		rsc.CacheClient, rsc.CacheConfig = c, c.Configuration()
		rsc.BackendOptions.MaxObjectSizeBytes = 2 * size
		ObjectProxyCacheRequest(&discardWriter{header: http.Header{}}, r.Clone(r.Context()))

		for name, rng := range map[string]string{"whole": "", "range": "bytes=1048576-2097151"} {
			want := int64(size)
			if rng != "" {
				want = 1 << 20
			}
			b.Run(provider+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					req := r.Clone(r.Context())
					if rng != "" {
						req.Header.Set(headers.NameRange, rng)
					}
					w := &discardWriter{header: http.Header{}}
					ObjectProxyCacheRequest(w, req)
					if w.n != want {
						b.Fatalf("%d bytes were served, of %d", w.n, want)
					}
				}
			})
		}
	}
}

// an object cached from an origin that sent no Content-Length is as long as its body, and
// every kind of range of it is answered as one of a known length
func TestRangesOfObjectCachedWithoutContentLength(t *testing.T) {
	body := streamBody()
	for _, provider := range []string{"memory", providers.Filesystem} {
		t.Run(provider, func(t *testing.T) {
			ts, _, r, rsc, err := setupTestHarnessOPC("", body, http.StatusOK, map[string]string{
				headers.NameCacheControl: "max-age=60", headers.NameContentType: streamContentType,
			})
			require.NoError(t, err)
			t.Cleanup(func() { closeTestHarness(ts, r) })
			if provider != "memory" {
				c := newDiskCache(t, provider)
				rsc.CacheClient, rsc.CacheConfig = c, c.Configuration()
			}
			h := &streamHarness{t: t, r: r, rsc: rsc}
			miss := h.do(http.MethodGet, nil)
			require.Equal(t, http.StatusOK, miss.code)
			require.Equal(t, body, miss.body)

			n := len(body)
			for rng, want := range map[string]struct {
				code     int
				body, cr string
			}{
				"bytes=100-199":   {http.StatusPartialContent, body[100:200], "bytes 100-199/" + strconv.Itoa(n)},
				"bytes=-500":      {http.StatusPartialContent, body[n-500:], "bytes " + strconv.Itoa(n-500) + "-" + strconv.Itoa(n-1) + "/" + strconv.Itoa(n)},
				"bytes=40000-":    {http.StatusPartialContent, body[40000:], "bytes 40000-" + strconv.Itoa(n-1) + "/" + strconv.Itoa(n)},
				"bytes=99999999-": {http.StatusRequestedRangeNotSatisfiable, "", "bytes */" + strconv.Itoa(n)},
			} {
				got := h.do(http.MethodGet, map[string]string{headers.NameRange: rng})
				require.Equal(t, want.code, got.code, rng)
				require.Equal(t, want.body, got.body, rng)
				require.Equal(t, want.cr, got.header.Get(headers.NameContentRange), rng)
			}
			multi := h.do(http.MethodGet, map[string]string{headers.NameRange: "bytes=0-9,-10"})
			require.Equal(t, http.StatusPartialContent, multi.code)
			require.Contains(t, multi.body, body[:10])
			require.Contains(t, multi.body, body[n-10:])
			require.Contains(t, multi.body, "bytes "+strconv.Itoa(n-10)+"-"+strconv.Itoa(n-1)+"/"+strconv.Itoa(n))
		})
	}
}

func TestWholeLength(t *testing.T) {
	d := &HTTPDocument{ContentLength: 7, Body: []byte("12")}
	require.Equal(t, int64(7), d.wholeLength(), "a recorded length stands")
	d.ContentLength = -1
	require.Equal(t, int64(2), d.wholeLength(), "a complete document is as long as its body")
	c := newDiskCache(t, providers.Filesystem)
	require.NoError(t, c.Store("k", []byte("1234"), time.Minute))
	_, body, _, err := c.(cache.StreamClient).OpenSplit("k")
	require.NoError(t, err)
	defer body.Close()
	d.deferred = body
	require.Equal(t, int64(4), d.wholeLength(), "or as its body in the cache")
	d.Ranges = byterange.Ranges{{Start: 0, End: 1}}
	require.Equal(t, int64(-1), d.wholeLength(), "a part of a document says nothing of the whole")
}
