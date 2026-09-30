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

	"github.com/trickstercache/trickster/v2/pkg/encoding/codecpool"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/klauspost/compress/zstd"
)

// a pooled encoder keeps a history of twice its window, 16 MiB at the default 8 MiB; RFC 9659 caps
// HTTP's window at 8 MiB, and responses need far less
const encoderWindow = 1 << 20

var (
	// a level can't be changed on a reused encoder, so each has its own pool
	encoderPools [zstd.SpeedBestCompression + 1]*codecpool.Encoders
	// one decoder at a time decodes in the caller's goroutine, and starts none of its own
	decoderPool = codecpool.NewDecoders(func() codecpool.Decoder {
		zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			panic("zstd: failed to create decoder: " + err.Error())
		}
		return zr
	})
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
	for l := zstd.SpeedFastest; l <= zstd.SpeedBestCompression; l++ {
		encoderPools[l] = codecpool.NewEncoders(func() codecpool.Encoder {
			zw, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(l),
				zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(encoderWindow))
			if err != nil {
				panic("zstd: failed to create encoder: " + err.Error())
			}
			return zw
		})
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

// NewEncoder returns a pooled encoder writing to w, which returns to its pool when closed
func NewEncoder(w io.Writer, level int) io.WriteCloser {
	return encoderPools[encoderLevel(level)].Get(w)
}

func encoderLevel(level int) zstd.EncoderLevel {
	switch {
	case level < 1, level == 3:
		return zstd.SpeedDefault
	case level < 3:
		return zstd.SpeedFastest
	case level < 8:
		return zstd.SpeedBetterCompression
	}
	return zstd.SpeedBestCompression
}

// NewDecoder returns a pooled decoder reading from r, which returns to its pool when closed
func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return decoderPool.Get(r)
}

// Detect reports whether in begins with an RFC 8878 Zstd frame magic
func Detect(in []byte) bool {
	return len(in) >= 4 && in[0] == 0x28 && in[1] == 0xb5 && in[2] == 0x2f && in[3] == 0xfd
}
