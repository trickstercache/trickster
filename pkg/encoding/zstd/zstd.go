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

package zstd

import (
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
)

var (
	commonDecoder *zstd.Decoder
	commonEncoder *zstd.Encoder
)

func init() {
	var err error
	commonDecoder, err = zstd.NewReader(nil)
	if err != nil {
		panic("zstd: failed to create decoder: " + err.Error())
	}
	commonEncoder, err = zstd.NewWriter(nil)
	if err != nil {
		panic("zstd: failed to create encoder: " + err.Error())
	}
}

func decodeBody(in []byte) ([]byte, error) {
	return commonDecoder.DecodeAll(in, nil)
}

// Decode returns the decoded version of the encoded byte slice.
func Decode(in []byte) ([]byte, error) {
	if !Detect(in) {
		return nil, zstd.ErrMagicMismatch
	}
	return decodeBody(in)
}

// Decompress returns decompressed bytes if b is zstd-framed,
// otherwise returns b unchanged.
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
	b := commonEncoder.EncodeAll(in, nil)
	return b, nil
}

func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level < 1 {
		level = 3
	}
	l := zstd.SpeedDefault
	switch {
	case level < 3:
		l = zstd.SpeedFastest
	case level > 3 && level < 8:
		l = zstd.SpeedBetterCompression
	case level > 7:
		l = zstd.SpeedBestCompression
	}
	zw, _ := zstd.NewWriter(w, zstd.WithEncoderLevel(l))
	return zw
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	zr, _ := zstd.NewReader(r)
	return reader.NewReadCloserResetter(zr)
}

// Detect reports whether in begins with an RFC 8878 Zstd frame magic
func Detect(in []byte) bool {
	return len(in) >= 4 && in[0] == 0x28 && in[1] == 0xb5 && in[2] == 0x2f && in[3] == 0xfd
}
