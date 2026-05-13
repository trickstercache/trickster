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
	"bytes"
	"net/http"
)

// DefaultMaxBytes is the per-writer cap applied by NewCaptureResponseWriterWithLimit
// when callers want defense-in-depth against pathological upstream responses
// blowing the heap during ALB fanout. 256 MiB is generous for legitimate
// time-series payloads and stops one bad backend from OOMing the proxy.
const DefaultMaxBytes = 256 * 1024 * 1024

// CaptureResponseWriter captures the response body to a byte slice
type CaptureResponseWriter struct {
	http.ResponseWriter
	header     http.Header
	statusCode int
	body       bytes.Buffer
	len        int
	maxBytes   int
	truncated  bool
}

// NewCaptureResponseWriter returns a new CaptureResponseWriter
func NewCaptureResponseWriter() *CaptureResponseWriter {
	return &CaptureResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

// NewCaptureResponseWriterWithLimit returns a CaptureResponseWriter that drops
// bytes past maxBytes and flips Truncated() to true. A non-positive maxBytes
// means unlimited.
func NewCaptureResponseWriterWithLimit(maxBytes int) *CaptureResponseWriter {
	return &CaptureResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
		maxBytes:   maxBytes,
	}
}

// Header returns the response header map
func (sw *CaptureResponseWriter) Header() http.Header {
	return sw.header
}

// WriteHeader sets the status code
func (sw *CaptureResponseWriter) WriteHeader(code int) {
	if code == 0 {
		code = http.StatusOK
	}
	sw.statusCode = code
}

// Write appends data to the response body. Returns len(b) even after the cap
// is reached so the upstream producer doesn't error or block; Truncated()
// surfaces the drop to the merge layer.
func (sw *CaptureResponseWriter) Write(b []byte) (int, error) {
	if sw.maxBytes > 0 && sw.len+len(b) > sw.maxBytes {
		if remaining := sw.maxBytes - sw.len; remaining > 0 {
			sw.body.Write(b[:remaining])
			sw.len += remaining
		}
		sw.truncated = true
		return len(b), nil
	}
	sw.body.Write(b)
	sw.len += len(b)
	return len(b), nil
}

// Body returns the captured response body
func (sw *CaptureResponseWriter) Body() []byte {
	return sw.body.Bytes()
}

// StatusCode returns the captured status code
func (sw *CaptureResponseWriter) StatusCode() int {
	if sw.statusCode == 0 {
		sw.statusCode = http.StatusOK
	}
	return sw.statusCode
}

// Truncated reports whether Write dropped bytes due to hitting maxBytes.
func (sw *CaptureResponseWriter) Truncated() bool {
	return sw.truncated
}
