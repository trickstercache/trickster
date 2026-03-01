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

package capture

import (
	"net/http"
	"sync"
)

// maxCaptureBodySize is the maximum body capacity to allow back into the pool.
// Writers that grew larger than this are discarded to prevent pool bloat.
const maxCaptureBodySize = 64 << 10 // 64 KB

var captureWriterPool = sync.Pool{
	New: func() any {
		return &CaptureResponseWriter{
			header:     make(http.Header),
			statusCode: http.StatusOK,
		}
	},
}

// GetCaptureResponseWriter returns a reset *CaptureResponseWriter from the pool.
func GetCaptureResponseWriter() *CaptureResponseWriter {
	crw := captureWriterPool.Get().(*CaptureResponseWriter)
	for k := range crw.header {
		delete(crw.header, k)
	}
	crw.statusCode = http.StatusOK
	crw.body.Reset()
	crw.len = 0
	crw.ResponseWriter = nil
	return crw
}

// PutCaptureResponseWriter returns a *CaptureResponseWriter to the pool.
// The caller must not use crw after this call.
func PutCaptureResponseWriter(crw *CaptureResponseWriter) {
	if crw == nil {
		return
	}
	if crw.body.Cap() > maxCaptureBodySize {
		return
	}
	captureWriterPool.Put(crw)
}
