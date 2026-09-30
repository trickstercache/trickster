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

package brotli

import (
	"bytes"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/codecpool"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/andybalholm/brotli"
)

const defaultLevel = 4

var (
	// a level can't be changed on a reused writer, so each has its own pool
	encoderPools [brotli.BestCompression + 1]*codecpool.Encoders
	decoderPool  = codecpool.NewDecoders(func() codecpool.Decoder { return brotli.NewReader(nil) })
)

func init() {
	for level := range encoderPools {
		encoderPools[level] = codecpool.NewEncoders(func() codecpool.Encoder {
			return brotli.NewWriterLevel(nil, level)
		})
	}
}

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	br := decoderPool.Get(bytes.NewReader(in))
	defer br.Close()
	return io.ReadAll(br)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	bw := NewEncoder(buf, defaultLevel)
	_, err := bw.Write(in)
	if cerr := bw.Close(); err == nil {
		err = cerr
	}
	return buf.Bytes(), err
}

// NewEncoder returns a pooled encoder writing to w, which returns to its pool when closed
func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level < 1 || level > brotli.BestCompression {
		level = defaultLevel
	}
	return encoderPools[level].Get(w)
}

// NewDecoder returns a pooled decoder reading from r, which returns to its pool when closed
func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return decoderPool.Get(r)
}
