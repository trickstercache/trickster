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
	"math/bits"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
)

// one large document must not hold its buffer's memory long after it was stored
const maxPooledCompressBuffer = 1 << 20

// brotli's best is some thousands to one, for a body of a single repeated byte; a claim beyond
// this sizes no allocation
const maxInflation = 1 << 16

// reads that return nothing before a decoder is taken to have stalled, as io.ReadAll allows none
const maxEmptyReads = 100

// the level cacheCodec compresses at, which spends encode time once per write for a smaller object
const cacheCompressionLevel = 6

const (
	// flagEncodedBit, set over a codec's provider bits, records what a stored document or body
	// was compressed with
	flagEncodedBit = 0x80
	// flagLegacyCompressed marks an object compressed before the codec was recorded, which was
	// always with brotli
	flagLegacyCompressed = 1
)

// what the cache compresses with; each object records its codec, so a change here still reads
// what was stored before, and a hit is served in the encoding it was stored in
var cacheCodec = providers.Brotli

var errSectionsCorrupt = errors.New("cached document sections are corrupt")

var compressPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func putCompressBuffer(buf *bytes.Buffer) {
	if buf.Cap() <= maxPooledCompressBuffer {
		buf.Reset()
		compressPool.Put(buf)
	}
}

// the flag byte for an object compressed with enc, which is 0 for one not compressed
func encodingFlag(enc providers.Provider) byte {
	if enc == providers.Identity {
		return 0
	}
	return flagEncodedBit | byte(enc)
}

// the codec a flag byte records, and false for a byte no write could have made
func flagEncoding(flag byte) (providers.Provider, bool) {
	switch {
	case flag == 0:
		return providers.Identity, true
	case flag == flagLegacyCompressed:
		return providers.Brotli, true
	case flag&flagEncodedBit == 0:
		return providers.Identity, false
	}
	enc := providers.Provider(flag &^ flagEncodedBit)
	return enc, bits.OnesCount8(byte(enc)) == 1 && providers.SelectDecoderInitializer(enc) != nil
}

// compresses b onto buf with the cache's codec
func encodeTo(buf *bytes.Buffer, b []byte) error {
	ei, _ := providers.SelectEncoderInitializer(cacheCodec)
	w := ei(buf, cacheCompressionLevel)
	_, err := w.Write(b)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// decodes b, which enc compressed; a size over zero is the length b decompresses to, which spares
// the result being grown to fit
func decodeAll(enc providers.Provider, b []byte, size int) ([]byte, error) {
	r := providers.SelectDecoderInitializer(enc)(bytes.NewReader(b))
	defer r.Close()
	if size <= 0 {
		return io.ReadAll(r)
	}
	out := make([]byte, size)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	// what is left must be the end of the stream, whose trailer and checksum are checked on reaching it
	var rest [1]byte
	for range maxEmptyReads {
		n, err := r.Read(rest[:])
		switch {
		case n > 0:
			return nil, errSectionsCorrupt
		case errors.Is(err, io.EOF):
			return out, nil
		case err != nil:
			return nil, err
		}
	}
	return nil, io.ErrNoProgress
}

func splitCache(c cache.Cache) (cache.SplitClient, bool) {
	sc, ok := c.(cache.SplitClient)
	return sc, ok && sc.SupportsSplit()
}

// the meta section is a byte that tells what the body is compressed with, if anything, the body's
// length and the rest of the document; the body is never copied
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
		if err := encodeTo(buf, body); err != nil {
			return err
		}
		meta[0], body = encodingFlag(cacheCodec), buf.Bytes()
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

// returns the length of the body once decoded, and what it was stored compressed with
func decodeMeta(d *HTTPDocument, meta []byte) (uint64, providers.Provider, error) {
	if len(meta) <= flagLen {
		return 0, providers.Identity, errSectionsCorrupt
	}
	enc, ok := flagEncoding(meta[0])
	if !ok {
		return 0, providers.Identity, errSectionsCorrupt
	}
	size, n := binary.Uvarint(meta[flagLen:])
	if n <= 0 {
		return 0, providers.Identity, errSectionsCorrupt
	}
	_, err := d.UnmarshalMsg(meta[flagLen+n:])
	emptyBodyIsNil(d)
	return size, enc, err
}

// the whole of an object, and what the body in the cache holds of it as enc stored it
func (d *HTTPDocument) canDefer(size uint64, enc providers.Provider, body cache.Body) bool {
	// an object whose origin gave no length for it is as long as its body
	// #nosec G115 -- a body's length is never negative
	whole := size > 0 && (d.ContentLength < 0 || uint64(d.ContentLength) == size) &&
		len(d.Ranges) == 0 && len(d.StoredRangeParts) == 0 && len(d.VaryNames) == 0 &&
		!d.IsMeta && !d.IsChunk
	// #nosec G115 -- a body's length is never negative
	return whole && (enc != providers.Identity || size == uint64(body.Size()))
}

// leaves the body in the cache when it can, for the response to read only what it serves; one stored
// compressed is left only when accept has its codec, and is sent as stored
func queryDeferred(sc cache.StreamClient, key string, accept providers.Provider) *queryResult {
	qr := &queryResult{queryKey: key, d: &HTTPDocument{}}
	var meta []byte
	var body cache.Body
	meta, body, qr.lookupStatus, qr.err = sc.OpenSplit(key)
	if qr.err != nil || qr.lookupStatus != status.LookupStatusHit {
		return qr
	}
	if len(meta) > 0 {
		var size uint64
		var enc providers.Provider
		size, enc, qr.err = decodeMeta(qr.d, meta)
		if qr.err == nil && (enc == providers.Identity || accept&enc != 0) && qr.d.canDefer(size, enc, body) {
			qr.d.deferred, qr.d.storedEncoding, qr.d.storedSize = body, enc, size
			if enc != providers.Identity && qr.d.ContentLength < 0 {
				// the length is the decoded body's, which ranges and revalidation are counted in
				// #nosec G115 -- bounded against the body's length when it is decoded
				qr.d.ContentLength = int64(size)
			}
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
	size, enc, err := decodeMeta(d, meta)
	if err != nil {
		return err
	}
	if body, err = decodeBody(enc, body, size); err != nil {
		return err
	}
	if len(body) > 0 {
		d.Body = body
	}
	return nil
}

// the body enc stored, decoded to its size
func decodeBody(enc providers.Provider, body []byte, size uint64) ([]byte, error) {
	if enc == providers.Identity {
		if size != uint64(len(body)) {
			return nil, errSectionsCorrupt
		}
		return body, nil
	}
	if size > uint64(len(body))*maxInflation {
		// no body compresses so well, and the length is not to be trusted with an allocation
		return nil, errSectionsCorrupt
	}
	// #nosec G115 -- the size was bounded against the body's length above
	return decodeAll(enc, body, int(size))
}
