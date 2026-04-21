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
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
)

var writerPool sync.Pool

// pooledWriter wraps a gzip.Writer and returns it to the pool on Close.
type pooledWriter struct {
	*gzip.Writer
}

func (pw *pooledWriter) Close() error {
	err := pw.Writer.Close()
	writerPool.Put(pw.Writer)
	return err
}

func decodeBody(in []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
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
	gw := gzip.NewWriter(buf)
	_, err := gw.Write(in)
	gw.Close()
	return buf.Bytes(), err
}

func NewEncoder(w io.Writer, level int) io.WriteCloser {
	if level == -1 {
		level = 6
	}
	if v := writerPool.Get(); v != nil {
		gw := v.(*gzip.Writer)
		gw.Reset(w)
		return &pooledWriter{gw}
	}
	gw, _ := gzip.NewWriterLevel(w, level)
	return &pooledWriter{gw}
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	rc, err := gzip.NewReader(r)
	if err != nil {
		return nil
	}
	return rc
}

// Detect reports whether in begins with an RFC 1952 gzip member header
func Detect(in []byte) bool {
	return len(in) >= 2 && in[0] == 0x1f && in[1] == 0x8b
}
