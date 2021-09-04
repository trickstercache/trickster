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
	"compress/flate"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
)

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	dr := flate.NewReader(bytes.NewReader(in))
	return io.ReadAll(dr)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	// NewWriter only returns an error if the second param is < -2
	dw, _ := flate.NewWriter(buf, -1)
	dw.Write(in)
	dw.Close()
	return buf.Bytes(), nil
}

func NewEncoder(w io.Writer, level int) io.WriteCloser {
	wc, _ := flate.NewWriter(w, level)
	return wc
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return reader.NewReadCloserResetter(flate.NewReader(r))
}
