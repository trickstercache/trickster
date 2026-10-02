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

package bytes

import (
	"io"
	"sync"
)

const (
	// ChunkSize is about how much output a ChunkWriter holds before writing it
	ChunkSize = 32 << 10
	// a buffer grown past this, for one very long element, is dropped rather than pooled
	maxPooledChunkBuffer = 1 << 20
)

var chunkBuffers = sync.Pool{New: func() any {
	b := make([]byte, 0, 2*ChunkSize)
	return &b
}}

// ChunkWriter builds output by appending to Buf, a pooled buffer, and writes it in parts of about
// ChunkSize, rather than making a small write for each element
type ChunkWriter struct {
	Buf []byte
	w   io.Writer
	bp  *[]byte
	err error
}

// NewChunkWriter returns a ChunkWriter that writes to w; it must be closed
func NewChunkWriter(w io.Writer) ChunkWriter {
	bp := chunkBuffers.Get().(*[]byte)
	return ChunkWriter{w: w, bp: bp, Buf: (*bp)[:0]}
}

// FlushIfFull writes Buf once it holds ChunkSize or more
func (cw *ChunkWriter) FlushIfFull() {
	if len(cw.Buf) >= ChunkSize {
		cw.Flush()
	}
}

// Flush writes Buf and empties it; after the first failed write, the rest are dropped
func (cw *ChunkWriter) Flush() {
	if len(cw.Buf) > 0 && cw.err == nil {
		_, cw.err = cw.w.Write(cw.Buf)
	}
	cw.Buf = cw.Buf[:0]
}

// Err reports the first failed write, after which nothing more is written
func (cw *ChunkWriter) Err() error {
	return cw.err
}

// Close writes what is left, returns the buffer to the pool, and reports the first failed write
func (cw *ChunkWriter) Close() error {
	cw.Flush()
	if cw.bp != nil && cap(cw.Buf) <= maxPooledChunkBuffer {
		*cw.bp = cw.Buf
		chunkBuffers.Put(cw.bp)
	}
	cw.bp, cw.Buf = nil, nil
	return cw.err
}
