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
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/andybalholm/brotli"
)

// one large document must not hold its buffer's memory long after it was stored
const maxPooledCompressBuffer = 1 << 20

// brotli's best is some thousands to one, for a body of a single repeated byte; a claim beyond
// this sizes no allocation
const maxInflation = 1 << 16

var errSectionsCorrupt = errors.New("cached document sections are corrupt")

var (
	compressPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
	deflaterPool = sync.Pool{New: func() any { return brotli.NewWriter(nil) }}
	inflaterPool = sync.Pool{New: func() any { return brotli.NewReader(nil) }}
)

func putCompressBuffer(buf *bytes.Buffer) {
	if buf.Cap() <= maxPooledCompressBuffer {
		buf.Reset()
		compressPool.Put(buf)
	}
}

func deflateTo(buf *bytes.Buffer, b []byte) error {
	w := deflaterPool.Get().(*brotli.Writer)
	w.Reset(buf)
	_, err := w.Write(b)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	w.Reset(nil)
	deflaterPool.Put(w)
	return err
}

// a size over zero is the length b decompresses to, which spares the result being grown to fit
func inflateAll(b []byte, size int) ([]byte, error) {
	r := inflaterPool.Get().(*brotli.Reader)
	r.Reset(bytes.NewReader(b))
	var out []byte
	var err error
	if size > 0 {
		out = make([]byte, size)
		if _, err = io.ReadFull(r, out); err == nil {
			// what is left must be the end of the stream, and nothing more
			var rest [1]byte
			if n, _ := r.Read(rest[:]); n > 0 {
				err = errSectionsCorrupt
			}
		}
	} else {
		out, err = io.ReadAll(r)
	}
	r.Reset(nil)
	inflaterPool.Put(r)
	return out, err
}

func splitCache(c cache.Cache) (cache.SplitClient, bool) {
	sc, ok := c.(cache.SplitClient)
	return sc, ok && sc.SupportsSplit()
}

// the meta section is a byte that tells whether the body is compressed, the body's length and
// the rest of the document; the body is never copied
func writeSections(sc cache.SplitClient, key string, d *HTTPDocument, compress bool, ttl time.Duration) error {
	m := d.ShallowCopy()
	body := m.Body
	m.Body = nil
	meta := make([]byte, flagLen, flagLen+binary.MaxVarintLen64+m.Msgsize())
	meta = binary.AppendUvarint(meta, uint64(len(body)))
	// skip compression for small payloads where overhead exceeds benefit
	if compress && len(body) >= minCompressLen {
		buf := compressPool.Get().(*bytes.Buffer)
		// every cache has done with the bytes it is given by the time it returns
		defer putCompressBuffer(buf)
		if err := deflateTo(buf, body); err != nil {
			return err
		}
		meta[0], body = flagCompressed, buf.Bytes()
	}
	meta, err := m.MarshalMsg(meta)
	if err != nil {
		return err
	}
	return sc.StoreSplit(key, meta, body, ttl)
}

func streamCache(c cache.Cache) (cache.StreamClient, bool) {
	sc, ok := c.(cache.StreamClient)
	return sc, ok && sc.SupportsStream()
}

// returns the length of the body, and whether it was stored compressed
func decodeMeta(d *HTTPDocument, meta []byte) (uint64, bool, error) {
	if len(meta) <= flagLen {
		return 0, false, errSectionsCorrupt
	}
	size, n := binary.Uvarint(meta[flagLen:])
	if n <= 0 {
		return 0, false, errSectionsCorrupt
	}
	_, err := d.UnmarshalMsg(meta[flagLen+n:])
	return size, meta[0] == flagCompressed, err
}

// the whole of an object, stored as it is served, and what the body in the cache holds
func (d *HTTPDocument) canDefer(size uint64, compressed bool, body cache.Body) bool {
	// an object whose origin gave no length for it is as long as its body
	// #nosec G115 -- a body's length is never negative
	return !compressed && size == uint64(body.Size()) && size > 0 &&
		(d.ContentLength == body.Size() || d.ContentLength < 0) &&
		len(d.Ranges) == 0 && len(d.StoredRangeParts) == 0 && len(d.VaryNames) == 0 &&
		!d.IsMeta && !d.IsChunk
}

// the body is left in the cache when it can be, for the response to read only what it serves
func queryDeferred(sc cache.StreamClient, key string) *queryResult {
	qr := &queryResult{queryKey: key, d: &HTTPDocument{}}
	var meta []byte
	var body cache.Body
	meta, body, qr.lookupStatus, qr.err = sc.OpenSplit(key)
	if qr.err != nil || qr.lookupStatus != status.LookupStatusHit {
		return qr
	}
	if len(meta) > 0 {
		var size uint64
		var compressed bool
		if size, compressed, qr.err = decodeMeta(qr.d, meta); qr.err == nil && qr.d.canDefer(size, compressed, body) {
			qr.d.deferred = body
			return qr
		}
	}
	var b []byte
	if qr.err == nil {
		b, qr.err = body.ReadAll()
	}
	body.Close()
	switch {
	case qr.err != nil:
	case len(meta) > 0:
		// the document is decoded anew, so that nothing of the first decoding is kept
		qr.d = &HTTPDocument{}
		qr.err = decodeSections(qr.d, meta, b)
	default:
		// an object that was stored whole
		qr.err = decodeDocument(qr.d, b)
	}
	return qr
}

// the document's body is the body section itself, unless that was compressed
func decodeSections(d *HTTPDocument, meta, body []byte) error {
	size, compressed, err := decodeMeta(d, meta)
	if err != nil {
		return err
	}
	if compressed {
		if size > uint64(len(body))*maxInflation {
			// no body compresses so well, and the length is not to be trusted with an allocation
			return errSectionsCorrupt
		}
		// #nosec G115 -- the size was bounded against the body's length above
		if body, err = inflateAll(body, int(size)); err != nil {
			return err
		}
	} else if size != uint64(len(body)) {
		return errSectionsCorrupt
	}
	if len(body) > 0 {
		d.Body = body
	}
	return nil
}
