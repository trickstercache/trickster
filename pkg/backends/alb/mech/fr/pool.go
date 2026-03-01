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

package fr

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
)

const (
	maxPoolSize = 128 // Don't pool for very large fanouts
	minPoolSize = 2   // Don't pool for tiny fanouts
)

// Pools for FR/FGR mechanism allocations
var (
	capturesPool = sync.Pool{
		New: func() any {
			slice := make([]*capture.CaptureResponseWriter, 0, 8)
			return &slice
		},
	}

	responseChannelPool = sync.Pool{
		New: func() any {
			return make(chan struct{}, 1)
		},
	}
)

// GetCapturesSlice gets a capture slice from pool or allocates new
func GetCapturesSlice(size int) []*capture.CaptureResponseWriter {
	if size > maxPoolSize || size < minPoolSize {
		return make([]*capture.CaptureResponseWriter, size)
	}

	sp := capturesPool.Get().(*[]*capture.CaptureResponseWriter)
	slice := *sp

	// Ensure capacity and set length
	if cap(slice) < size {
		capturesPool.Put(sp)
		return make([]*capture.CaptureResponseWriter, size)
	}

	// Reslice to requested size and clear any existing data
	slice = slice[:size]
	for i := range slice {
		slice[i] = nil
	}
	return slice
}

// PutCapturesSlice returns a capture slice to the pool
func PutCapturesSlice(slice []*capture.CaptureResponseWriter) {
	if cap(slice) > maxPoolSize || cap(slice) < minPoolSize {
		return
	}

	// Return each writer to the capture pool, then clear the pointer
	for i := range slice {
		capture.PutCaptureResponseWriter(slice[i])
		slice[i] = nil
	}

	// Reset to zero length but keep capacity
	slice = slice[:0]
	capturesPool.Put(&slice)
}

// getResponseChannel gets a channel from pool or allocates new
func getResponseChannel() chan struct{} {
	ch := responseChannelPool.Get().(chan struct{})
	// Ensure channel is drained
	select {
	case <-ch:
	default:
	}
	return ch
}

// putResponseChannel returns a channel to the pool
func putResponseChannel(ch chan struct{}) {
	// Drain any pending message before returning to pool
	select {
	case <-ch:
	default:
	}
	responseChannelPool.Put(ch)
}
