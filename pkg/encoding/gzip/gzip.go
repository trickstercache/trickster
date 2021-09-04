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
	"compress/gzip"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
)

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return []byte{}, err
	}
	return io.ReadAll(gr)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(in)))
	gw := gzip.NewWriter(buf)
	_, err := gw.Write(in)
	gw.Close()
	return buf.Bytes(), err
}

func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level == -1 {
		level = 6
	}
	wc, _ := gzip.NewWriterLevel(w, level)
	return wc
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	rc, err := gzip.NewReader(r)
	if err != nil {
		return nil
	}
	return rc
}
