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

// Package zlib provides zlib (RFC 1950) capabilities for byte slices.
package zlib

import (
	"bytes"
	stdzlib "compress/zlib"
	"errors"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// ErrInvalidHeader indicates the input does not begin with a valid zlib wrapper.
var ErrInvalidHeader = errors.New("zlib: invalid header")

// Detect reports whether in begins with a plausible zlib stream header (RFC 1950 CMF/FLG).
func Detect(in []byte) bool {
	if len(in) < 2 {
		return false
	}
	cmf, flg := in[0], in[1]
	if cmf&0x0f != 8 { // compression method must be deflate
		return false
	}
	return (int(cmf)*256+int(flg))%31 == 0
}

func decodeBody(in []byte) ([]byte, error) {
	r, err := stdzlib.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Decode returns the decoded version of the zlib-wrapped byte slice.
func Decode(in []byte) ([]byte, error) {
	if !Detect(in) {
		return nil, ErrInvalidHeader
	}
	return decodeBody(in)
}

// Decompress returns decompressed bytes if b is zlib-wrapped, otherwise returns b unchanged.
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

// Encode returns the zlib-wrapped version of the byte slice.
func Encode(in []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := stdzlib.NewWriter(&buf)
	if _, err := w.Write(in); err != nil {
		if err2 := w.Close(); err2 != nil {
			logger.Error("failed to close encoder writer",
				logging.Pairs{"error": err2, "parentError": err})
		}
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// NewEncoder returns a zlib writer; level is passed to compress/zlib.NewWriterLevel (-1 for default).
func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level < stdzlib.HuffmanOnly || level > stdzlib.BestCompression {
		level = stdzlib.DefaultCompression
	}
	zw, _ := stdzlib.NewWriterLevel(w, level)
	return zw
}

// NewDecoder returns a resettable read closer for zlib streams.
func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	zr, err := stdzlib.NewReader(r)
	if err != nil {
		return nil
	}
	return reader.NewReadCloserResetter(zr)
}
