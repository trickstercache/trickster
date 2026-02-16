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

package engines

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// maxCacheBufferSize is the maximum buffer capacity to allow back into the pool.
// Buffers that grew beyond this are discarded to prevent pool bloat.
// Set to 2x the default MaxObjectSizeBytes (524288).
const maxCacheBufferSize = 1 << 20 // 1 MB

var cacheBufferPool = sync.Pool{
	New: func() any { return &bytes.Buffer{} },
}

func getCacheBuffer() *bytes.Buffer {
	buf := cacheBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func putCacheBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	if buf.Cap() > maxCacheBufferSize {
		return
	}
	buf.Reset()
	cacheBufferPool.Put(buf)
}

// Origin slice pools
//
// The origin slices (originRequests, originResponses, originReaders) are allocated
// per upstream fanout, typically holding 1–N items. Pooling reuses their backing
// arrays across requests, reducing allocation pressure for parallel range requests.

const maxOriginSliceCap = 64 // don't pool slices from unusually large fanouts

var (
	originRequestsPool = sync.Pool{
		New: func() any {
			s := make([]*http.Request, 0, 4)
			return &s
		},
	}
	originResponsesPool = sync.Pool{
		New: func() any {
			s := make([]*http.Response, 0, 4)
			return &s
		},
	}
	originReadersPool = sync.Pool{
		New: func() any {
			s := make([]io.ReadCloser, 0, 4)
			return &s
		},
	}
)

// getRequestSlice returns a zero-length []*http.Request with capacity >= n.
// If the pooled slice lacks sufficient capacity, it is returned and a fresh
// slice is allocated.
func getRequestSlice(n int) []*http.Request {
	sp := originRequestsPool.Get().(*[]*http.Request)
	s := *sp
	if cap(s) >= n {
		return s[:0]
	}
	originRequestsPool.Put(sp)
	return make([]*http.Request, 0, n)
}

// putRequestSlice returns s to the pool after clearing all pointer elements.
func putRequestSlice(s []*http.Request) {
	if s == nil || cap(s) > maxOriginSliceCap {
		return
	}
	for i := range s {
		s[i] = nil
	}
	s = s[:0]
	originRequestsPool.Put(&s)
}

// getResponseSlice returns a []*http.Response of length n with all elements nil'd.
// If the pooled slice lacks capacity, a fresh slice is allocated.
func getResponseSlice(n int) []*http.Response {
	sp := originResponsesPool.Get().(*[]*http.Response)
	s := *sp
	if cap(s) >= n {
		s = s[:n]
		for i := range s {
			s[i] = nil
		}
		return s
	}
	originResponsesPool.Put(sp)
	return make([]*http.Response, n)
}

// putResponseSlice returns s to the pool after clearing all pointer elements.
func putResponseSlice(s []*http.Response) {
	if s == nil || cap(s) > maxOriginSliceCap {
		return
	}
	for i := range s {
		s[i] = nil
	}
	s = s[:0]
	originResponsesPool.Put(&s)
}

// getReadCloserSlice returns a []io.ReadCloser of length n with all elements nil'd.
// If the pooled slice lacks capacity, a fresh slice is allocated.
func getReadCloserSlice(n int) []io.ReadCloser {
	sp := originReadersPool.Get().(*[]io.ReadCloser)
	s := *sp
	if cap(s) >= n {
		s = s[:n]
		for i := range s {
			s[i] = nil
		}
		return s
	}
	originReadersPool.Put(sp)
	return make([]io.ReadCloser, n)
}

// putReadCloserSlice returns s to the pool after clearing all interface elements.
func putReadCloserSlice(s []io.ReadCloser) {
	if s == nil || cap(s) > maxOriginSliceCap {
		return
	}
	for i := range s {
		s[i] = nil
	}
	s = s[:0]
	originReadersPool.Put(&s)
}

// HTTPDocument msgp serialization buffer pool
//
// Pooling the []byte output of d.MarshalMsg avoids a fresh allocation on every
// cache write. Stored as *[]byte to preserve capacity across pool round-trips.

// maxHTTPDocMarshalBufSize is the maximum buffer capacity to allow back into the
// pool. Set to 2x DefaultMaxObjectSizeBytes (524288) with headroom for headers.
const maxHTTPDocMarshalBufSize = 1 << 20 // 1 MB

var httpDocMarshalBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

func getHTTPDocMarshalBuf() []byte {
	bp := httpDocMarshalBufPool.Get().(*[]byte)
	return (*bp)[:0]
}

// putHTTPDocMarshalBuf returns b to the pool. b must not be used after this call.
func putHTTPDocMarshalBuf(b []byte) {
	if cap(b) > maxHTTPDocMarshalBufSize {
		return
	}
	httpDocMarshalBufPool.Put(&b)
}

// Cache key values pool
//
// DeriveCacheKey allocates a []string to collect cache key components. Pooling
// reuses these slices across requests.

const maxCacheKeyValuesSliceCap = 200

var cacheKeyValuesPool = sync.Pool{
	New: func() any {
		s := make([]string, 0, 16)
		return &s
	},
}

// getCacheKeyValues returns a zero-length []string with capacity for appending
// cache key values. The slice backing array is reused across calls.
func getCacheKeyValues() []string {
	sp := cacheKeyValuesPool.Get().(*[]string)
	return (*sp)[:0]
}

// putCacheKeyValues returns the slice to the pool. The slice must not be used
// after this call. Oversized slices are discarded.
func putCacheKeyValues(s []string) {
	if cap(s) > maxCacheKeyValuesSliceCap {
		return
	}
	// Clear string references to allow GC
	for i := range s {
		s[i] = ""
	}
	s = s[:0]
	cacheKeyValuesPool.Put(&s)
}
