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

// Package gzip provides gzip capabilities for byte slices
package gzip

import (
	"bytes"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/codecpool"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/klauspost/compress/gzip"
)

const defaultLevel = 6

var (
	// a level can't be changed on a reused writer, so each has its own pool
	encoderPools [gzip.BestCompression - gzip.StatelessCompression + 1]*codecpool.Encoders
	decoderPool  = codecpool.NewDecoders(func() codecpool.Decoder { return new(gzip.Reader) })
)

func init() {
	for i := range encoderPools {
		level := i + gzip.StatelessCompression
		encoderPools[i] = codecpool.NewEncoders(func() codecpool.Encoder {
			gw, _ := gzip.NewWriterLevel(nil, level)
			return gw
		})
	}
}

func decodeBody(in []byte) ([]byte, error) {
	gr := decoderPool.Get(bytes.NewReader(in))
	defer gr.Close()
	return io.ReadAll(gr)
}

// Decode returns the decoded version of the encoded byte slice.
func Decode(in []byte) ([]byte, error) {
	if !Detect(in) {
		return nil, gzip.ErrHeader
	}
	return decodeBody(in)
}

// Decompress returns decompressed bytes if b is gzip-encoded, otherwise returns b unchanged.
func Decompress(b []byte) []byte {
	if !Detect(b) {
		return b
	}
	out, err := decodeBody(b)
	if err != nil {
		return b
	}
	return out
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	gw := NewEncoder(buf, -1)
	_, err := gw.Write(in)
	if cerr := gw.Close(); err == nil {
		err = cerr
	}
	return buf.Bytes(), err
}

// NewEncoder returns a pooled encoder writing to w, which returns to its pool when closed
func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level == gzip.DefaultCompression || level < gzip.StatelessCompression ||
		level > gzip.BestCompression {
		level = defaultLevel
	}
	return encoderPools[level-gzip.StatelessCompression].Get(w)
}

// NewDecoder returns a pooled decoder reading from r, which returns to its pool when closed.
// Reads fail if r doesn't begin with a gzip header.
func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return decoderPool.Get(r)
}

// Detect reports whether in begins with an RFC 1952 gzip member header
func Detect(in []byte) bool {
	return len(in) >= 2 && in[0] == 0x1f && in[1] == 0x8b
}
