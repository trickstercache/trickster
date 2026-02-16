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

package headers

import (
	"net/http"
	"sync"
)

// maxHeaderEntries is the maximum number of header entries to allow back into the pool.
// Headers that grew beyond this are discarded to prevent pool bloat.
const maxHeaderEntries = 50

var headerPool = sync.Pool{
	New: func() any { return make(http.Header) },
}

func getHeader() http.Header {
	h := headerPool.Get().(http.Header)
	// Clear existing entries
	for k := range h {
		delete(h, k)
	}
	return h
}

func putHeader(h http.Header) {
	if h == nil {
		return
	}
	if len(h) > maxHeaderEntries {
		return
	}
	headerPool.Put(h)
}

// Small string slice pool for hop parts formatting
//
// The Hop.String() method allocates a []string to collect forwarding header
// components. This pool reuses small (4-element) slices for this purpose.

var hopPartsPool = sync.Pool{
	New: func() any {
		s := make([]string, 4)
		return &s
	},
}

// getHopParts returns a []string of length 4 for collecting hop parts.
// The slice is cleared and ready to use.
func getHopParts() []string {
	sp := hopPartsPool.Get().(*[]string)
	s := *sp
	for i := range s {
		s[i] = ""
	}
	return s
}

// putHopParts returns a []string to the pool. Only accepts slices with capacity 4.
func putHopParts(s []string) {
	if cap(s) != 4 {
		return
	}
	hopPartsPool.Put(&s)
}
