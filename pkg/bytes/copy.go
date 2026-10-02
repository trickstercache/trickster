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
	"errors"
	"io"
	"sync"
)

// CopyBufferSize is the length of the buffers Copy lends, which is io.Copy's own
const CopyBufferSize = 32 << 10

var copyBuffers = sync.Pool{New: func() any {
	b := make([]byte, CopyBufferSize)
	return &b
}}

// GetCopyBuffer lends a buffer of CopyBufferSize bytes, which PutCopyBuffer returns
func GetCopyBuffer() *[]byte {
	return copyBuffers.Get().(*[]byte)
}

// PutCopyBuffer returns a buffer lent by GetCopyBuffer; one of another size is dropped
func PutCopyBuffer(bp *[]byte) {
	if cap(*bp) != CopyBufferSize {
		return
	}
	*bp = (*bp)[:CopyBufferSize]
	copyBuffers.Put(bp)
}

// Copy is io.Copy, but with a pooled buffer when neither side can copy without one
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	if _, ok := src.(io.WriterTo); ok {
		return io.Copy(dst, src)
	}
	if _, ok := dst.(io.ReaderFrom); ok {
		return io.Copy(dst, src)
	}
	bp := GetCopyBuffer()
	n, err := io.CopyBuffer(dst, src, *bp)
	PutCopyBuffer(bp)
	return n, err
}

// ReadAllSized is io.ReadAll for a reader expected to hold size bytes, which it reads into one
// allocation when the size is right; a caller bounds size, as it may be a peer's claim
func ReadAllSized(r io.Reader, size int64) ([]byte, error) {
	if size <= 0 {
		return io.ReadAll(r)
	}
	// one byte over, so the read that finds the end has room without growing the slice
	b := make([]byte, 0, size+1)
	for {
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return b, err
		}
		if len(b) == cap(b) {
			// longer than it claimed, so it grows as io.ReadAll's would
			b = append(b, 0)[:len(b)]
		}
	}
}
