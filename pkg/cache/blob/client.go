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

package blob

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/header"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

const (
	headerBufferSize = 256

	eventInvalid  = "invalid_object"
	reasonCorrupt = "corrupt"
	reasonExpired = "expired"
	reasonFormat  = "unknown_format"
)

// DeferredVerifyLen is the payload length from which the checksum is verified after the object
// is returned, so a large read is not held for it; a damaged object is served once
const DeferredVerifyLen = 10 << 20

// ErrKeyRequired is returned when an object is stored without a cache key
var ErrKeyRequired = errors.New("cache key required")

var (
	_ cache.Client       = (*Client)(nil)
	_ cache.Scanner      = (*Client)(nil)
	_ cache.SplitClient  = (*Client)(nil)
	_ cache.StreamClient = (*Client)(nil)
)

var (
	headerPool = sync.Pool{New: func() any {
		b := make([]byte, 0, headerBufferSize)
		return &b
	}}
	prefixPool = sync.Pool{New: func() any {
		b := make([]byte, header.PrefixLen)
		return &b
	}}
)

// Client is a cache.Client that stores each object in a Store as a self-describing frame,
// and so can tell a whole, current object from a torn, foreign or expired one
type Client struct {
	store    Store
	name     string
	provider string
	// verifying counts the checks of large payloads still running after their reads returned
	verifying sync.WaitGroup
}

// NewClient returns a Client that frames the objects of the named cache into store
func NewClient(store Store, cacheName, cacheProvider string) *Client {
	return &Client{store: store, name: cacheName, provider: cacheProvider}
}

// Connect readies the underlying Store for use
func (c *Client) Connect() error {
	return c.store.Connect()
}

// Close releases the underlying Store, once the checks of large payloads still running are done
func (c *Client) Close() error {
	c.verifying.Wait()
	return c.store.Close()
}

// MetaStore returns the underlying Store's metadata file capability, when it has one
func (c *Client) MetaStore() (MetaStore, bool) {
	ms, ok := c.store.(MetaStore)
	return ms, ok
}

// FreeBytes returns the space left on the Store's medium, and false when it cannot say
func (c *Client) FreeBytes() (int64, bool) {
	fs, ok := c.store.(FreeSpacer)
	if !ok {
		return 0, false
	}
	n, err := fs.FreeBytes()
	return n, err == nil
}

// Store writes data at cacheKey, to expire after ttl. A ttl of zero or less never expires.
func (c *Client) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.StoreSplit(cacheKey, nil, data, ttl)
}

// StoreSplit writes an object at cacheKey in two sections, so that a reader can later
// have the body alone. A ttl of zero or less never expires.
func (c *Client) StoreSplit(cacheKey string, meta, body []byte, ttl time.Duration) error {
	if cacheKey == "" {
		return ErrKeyRequired
	}
	now := time.Now()
	h := header.Header{
		LastWrite: now.UnixNano(),
		// #nosec G115 -- a section over 4 GiB fails the length check below
		MetaLen:    uint32(len(meta)),
		BodyLen:    uint64(len(body)),
		PayloadCRC: header.Checksum(meta, body),
	}
	if int(h.MetaLen) != len(meta) {
		return header.ErrCorrupt
	}
	if ttl > 0 {
		h.Expiration = now.Add(ttl).UnixNano()
	}
	bp := headerPool.Get().(*[]byte)
	hdr, err := header.Append((*bp)[:0], cacheKey, &h)
	if err == nil {
		err = c.store.Put(cacheKey, hdr, meta, body)
	}
	if cap(hdr) <= headerBufferSize {
		*bp = hdr[:0]
	}
	headerPool.Put(bp)
	return err
}

// SupportsSplit reports true: every object is stored as two sections, one of which may be empty
func (*Client) SupportsSplit() bool {
	return true
}

// Retrieve returns the object stored at cacheKey. An object that is expired, torn or not
// the one asked for is a miss.
func (c *Client) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	_, payload, s, err := c.retrieve(cacheKey)
	return payload, s, err
}

// RetrieveSplit returns the two sections of the object stored at cacheKey, which share
// one buffer. An object that is expired, torn or not the one asked for is a miss.
func (c *Client) RetrieveSplit(cacheKey string) ([]byte, []byte, status.LookupStatus, error) {
	metaLen, payload, s, err := c.retrieve(cacheKey)
	if err != nil {
		return nil, nil, s, err
	}
	return payload[:metaLen:metaLen], payload[metaLen:], s, nil
}

// the payload is returned with the length of its meta section, which leads the body
func (c *Client) retrieve(cacheKey string) (int, []byte, status.LookupStatus, error) {
	b, err := c.store.Open(cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			return 0, nil, status.LookupStatusKeyMiss, cache.ErrKNF
		}
		return 0, nil, status.LookupStatusError, err
	}
	size := b.Size()
	buf := make([]byte, size)
	n, err := b.ReadAt(buf, 0)
	b.Close()
	if err != nil && (!errors.Is(err, io.EOF) || int64(n) < size) {
		return 0, nil, status.LookupStatusError, err
	}
	h, key, err := header.Parse(buf, size)
	if err != nil {
		return c.discard(cacheKey, reasonOf(err), nil)
	}
	if string(key) != cacheKey {
		// another key's object sits where this key's would; it is whole, and is left alone
		return 0, nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	if h.Expired(time.Now().UnixNano()) {
		return c.discard(cacheKey, reasonExpired, &h)
	}
	payload := buf[h.PayloadOffset():]
	if len(payload) >= DeferredVerifyLen {
		c.verifyLater(cacheKey, h, payload)
	} else if header.VerifyPayload(&h, payload) != nil {
		return c.discard(cacheKey, reasonCorrupt, &h)
	}
	return int(h.MetaLen), payload, status.LookupStatusHit, nil
}

// the caller has the payload already, to read and not to write, which is what lets the check
// share it
func (c *Client) verifyLater(cacheKey string, h header.Header, payload []byte) {
	c.verifying.Add(1)
	safego.Go(c.verifyPanicked, func() {
		defer c.verifying.Done()
		if header.VerifyPayload(&h, payload) != nil {
			c.discard(cacheKey, reasonCorrupt, &h)
		}
	})
}

func (c *Client) verifyPanicked(r any, _ []byte) {
	logger.Error("cache checksum verification panic",
		logging.Pairs{keys.CacheName: c.name, "panic": r})
}

// SupportsStream reports true: a Store whose objects may not stay open has them opened
// anew, and briefly, for each part that is read
func (*Client) SupportsStream() bool {
	return true
}

// ErrObjectChanged is returned by a read of a Body whose object was written again, or
// removed, since the Body was opened
var ErrObjectChanged = errors.New("cached object changed while it was read")

// where the body lies in the frame, and what the header said of it
type section struct {
	offset, size int64
	lastWrite    int64
	sum          uint32
	meta         []byte
}

func (s *section) verify(out []byte, n int, err error) ([]byte, error) {
	if err != nil && (!errors.Is(err, io.EOF) || n < len(out)) {
		return nil, err
	}
	if header.Checksum(s.meta, out) != s.sum {
		return nil, header.ErrCorrupt
	}
	return out, nil
}

// the Blob stays open until the body is closed
type openBody struct {
	*io.SectionReader
	section
	blob Blob
}

func (b *openBody) Close() error {
	return b.blob.Close()
}

// ReadAll returns the whole body, verified along with the meta section against the
// checksum the object was stored with
func (b *openBody) ReadAll() ([]byte, error) {
	out := make([]byte, b.size)
	n, err := b.ReadAt(out, 0)
	return b.verify(out, n, err)
}

// each read opens the object anew for as long as the read takes, and fails if it is no longer
// the same object
type briefBody struct {
	section
	client *Client
	key    string
	// next is where the next sequential read begins
	next int64
}

func (b *briefBody) Size() int64 {
	return b.size
}

func (*briefBody) Close() error {
	return nil
}

func (b *briefBody) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= b.size {
		return 0, io.EOF
	}
	short := int64(len(p)) > b.size-off
	if short {
		p = p[:b.size-off]
	}
	blob, err := b.client.store.Open(b.key)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			err = ErrObjectChanged
		}
		return 0, err
	}
	defer blob.Close()
	var prefix [header.Size]byte
	if _, err = blob.ReadAt(prefix[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	// the header is its own check, so that of the key after it is left to the first read
	if h, ok := header.Peek(prefix[:]); !ok || h.LastWrite != b.lastWrite || h.PayloadCRC != b.sum ||
		h.BodyOffset() != b.offset || blob.Size() != b.offset+b.size {
		return 0, ErrObjectChanged
	}
	n, err := blob.ReadAt(p, b.offset+off)
	if err == nil && short {
		err = io.EOF
	}
	return n, err
}

func (b *briefBody) Read(p []byte) (int, error) {
	n, err := b.ReadAt(p, b.next)
	b.next += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		// the end is reported by the read that finds nothing left
		err = nil
	}
	return n, err
}

// ReadAll returns the whole body, verified along with the meta section against the
// checksum the object was stored with
func (b *briefBody) ReadAll() ([]byte, error) {
	out := make([]byte, b.size)
	if b.size == 0 {
		return b.verify(out, 0, nil)
	}
	n, err := b.ReadAt(out, 0)
	return b.verify(out, n, err)
}

// OpenSplit returns the meta section of the object stored at cacheKey, and its body unread.
// An object that is expired, torn or not the one asked for is a miss.
func (c *Client) OpenSplit(cacheKey string) ([]byte, cache.Body, status.LookupStatus, error) {
	b, err := c.store.Open(cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			return nil, nil, status.LookupStatusKeyMiss, cache.ErrKNF
		}
		return nil, nil, status.LookupStatusError, err
	}
	meta, h, s, err := c.openMeta(b, cacheKey)
	if err != nil {
		b.Close()
		return nil, nil, s, err
	}
	sec := section{
		// #nosec G115 -- the header was parsed against the blob's size, which bounds the body's length
		offset: h.BodyOffset(), size: int64(h.BodyLen), lastWrite: h.LastWrite, sum: h.PayloadCRC, meta: meta,
	}
	if !c.store.Streamable() {
		b.Close()
		return meta, &briefBody{section: sec, client: c, key: cacheKey}, s, nil
	}
	return meta, &openBody{SectionReader: io.NewSectionReader(b, sec.offset, sec.size), section: sec, blob: b}, s, nil
}

// one read covers the header, key and meta section when they fit the prefix buffer, which they
// usually do
func (c *Client) openMeta(b Blob, cacheKey string) ([]byte, header.Header, status.LookupStatus, error) {
	bp := prefixPool.Get().(*[]byte)
	defer prefixPool.Put(bp)
	prefix := *bp
	size := b.Size()
	if int64(len(prefix)) > size {
		prefix = prefix[:size]
	}
	n, err := b.ReadAt(prefix, 0)
	if err != nil && (!errors.Is(err, io.EOF) || n < len(prefix)) {
		return nil, header.Header{}, status.LookupStatusError, err
	}
	h, key, err := header.Parse(prefix, size)
	if err != nil || h.Expired(time.Now().UnixNano()) {
		reason := reasonExpired
		if err != nil {
			reason = reasonOf(err)
		}
		found := &h
		if err != nil {
			found = nil
		}
		_, _, s, err := c.discard(cacheKey, reason, found)
		return nil, h, s, err
	}
	if string(key) != cacheKey {
		return nil, h, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	meta := make([]byte, h.MetaLen)
	if h.BodyOffset() <= int64(len(prefix)) {
		copy(meta, prefix[h.PayloadOffset():])
		return meta, h, status.LookupStatusHit, nil
	}
	if _, err = b.ReadAt(meta, h.PayloadOffset()); err != nil {
		return nil, h, status.LookupStatusError, err
	}
	return meta, h, status.LookupStatusHit, nil
}

func reasonOf(err error) string {
	if errors.Is(err, header.ErrBadMagic) {
		return reasonFormat
	}
	return reasonCorrupt
}

// found names the object judged unservable, so that one stored in its place since is not
// removed; nil means the frame could not be read as one
func (c *Client) discard(cacheKey, reason string, found *header.Header) (int, []byte, status.LookupStatus, error) {
	metrics.ObserveCacheEvent(c.name, c.provider, eventInvalid, reason)
	// an object that cannot be removed is a miss all the same, and is found again by a sweep
	_ = c.store.DeleteIf(cacheKey, func(b Blob) bool {
		bp := prefixPool.Get().(*[]byte)
		defer prefixPool.Put(bp)
		h, _, err := header.Read(b, b.Size(), *bp)
		if err != nil {
			// still not a whole frame
			return found == nil
		}
		return found != nil && h.LastWrite == found.LastWrite && h.PayloadCRC == found.PayloadCRC
	})
	return 0, nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

// Remove deletes the objects stored at cacheKeys
func (c *Client) Remove(cacheKeys ...string) error {
	return c.store.Delete(cacheKeys...)
}

// ScanMeta passes what up to limit stored objects say of themselves to fn, reading no
// payloads, and removes those that are expired or unreadable as it meets them
func (c *Client) ScanMeta(after string, limit int, fn func(cache.ObjectMeta)) (string, bool, error) {
	bp := prefixPool.Get().(*[]byte)
	defer prefixPool.Put(bp)
	nowNano := time.Now().UnixNano()
	return c.store.Scan(after, limit, func(b Blob) bool {
		h, key, err := header.Read(b, b.Size(), *bp)
		if err != nil {
			metrics.ObserveCacheEvent(c.name, c.provider, eventInvalid, reasonOf(err))
			return true
		}
		if h.Expired(nowNano) {
			return true
		}
		m := cache.ObjectMeta{Key: string(key), Size: h.PayloadLen(), LastWrite: time.Unix(0, h.LastWrite)}
		if h.Expiration != 0 {
			m.Expiration = time.Unix(0, h.Expiration)
		}
		fn(m)
		return false
	})
}
