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

package deflate

import (
	"bytes"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/codecpool"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/klauspost/compress/flate"
)

// a level can't be changed on a reused writer, so each has its own pool
var encoderPools [flate.BestCompression - flate.HuffmanOnly + 1]*codecpool.Encoders

func init() {
	for i := range encoderPools {
		level := i + flate.HuffmanOnly
		encoderPools[i] = codecpool.NewEncoders(func() codecpool.Encoder {
			fw, _ := flate.NewWriter(nil, level)
			return fw
		})
	}
}

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	dr := flate.NewReader(bytes.NewReader(in))
	return io.ReadAll(dr)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	dw := NewEncoder(buf, flate.DefaultCompression)
	_, err := dw.Write(in)
	if cerr := dw.Close(); err == nil {
		err = cerr
	}
	return buf.Bytes(), err
}

// NewEncoder returns a pooled encoder writing to w, which returns to its pool when closed
func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level < flate.HuffmanOnly || level > flate.BestCompression {
		level = flate.DefaultCompression
	}
	return encoderPools[level-flate.HuffmanOnly].Get(w)
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return reader.NewReadCloserResetter(flate.NewReader(r))
}
