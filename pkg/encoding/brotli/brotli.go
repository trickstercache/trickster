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

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/andybalholm/brotli"
)

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	br := brotli.NewReader(bytes.NewReader(in))
	return io.ReadAll(br)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	bw := brotli.NewWriter(buf)
	_, err := bw.Write(in)
	bw.Close()
	return buf.Bytes(), err
}

func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level < 1 {
		level = 4
	}
	return brotli.NewWriterLevel(w, level)
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return reader.NewReadCloserResetter(brotli.NewReader(r))
}
