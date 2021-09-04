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

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/klauspost/compress/zstd"
)

var commonDecoder *zstd.Decoder
var commonEncoder *zstd.Encoder

func init() {
	commonDecoder, _ = zstd.NewReader(nil)
	commonEncoder, _ = zstd.NewWriter(nil)
}

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	return commonDecoder.DecodeAll(in, nil)
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
	if level < 3 {
		l = zstd.SpeedFastest
	} else if level > 3 && level < 8 {
		l = zstd.SpeedBetterCompression
	} else if level > 7 {
		l = zstd.SpeedBestCompression
	}
	zw, _ := zstd.NewWriter(w, zstd.WithEncoderLevel(l))
	return zw
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	zr, _ := zstd.NewReader(r)
	return reader.NewReadCloserResetter(zr)
}
