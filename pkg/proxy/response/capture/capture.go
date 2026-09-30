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
	"strconv"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
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
	// presized is set once WriteHeader has sized the body, which it does only once
	presized bool
}

// NewCaptureResponseWriter returns a new CaptureResponseWriter
func NewCaptureResponseWriter() *CaptureResponseWriter {
	return &CaptureResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

// a writer whose body grew past this is dropped when released, so one large response doesn't keep
// its memory
const maxPooledBody = 1 << 20

var writers = sync.Pool{New: func() any {
	return &CaptureResponseWriter{header: make(http.Header)}
}}

// NewCaptureResponseWriterWithLimit returns a writer, perhaps a released one, that drops bytes past
// maxBytes and flips Truncated() to true; a non-positive maxBytes means unlimited
func NewCaptureResponseWriterWithLimit(maxBytes int) *CaptureResponseWriter {
	sw := writers.Get().(*CaptureResponseWriter)
	sw.statusCode, sw.maxBytes = http.StatusOK, maxBytes
	return sw
}

// Release lets the writer be reused. It is called at most once, after the response is complete, and
// neither the writer nor what its Header or Body returned may be used after it.
func (sw *CaptureResponseWriter) Release() {
	if sw.body.Cap() > maxPooledBody {
		return
	}
	sw.ResponseWriter = nil
	clear(sw.header)
	sw.body.Reset()
	sw.len, sw.maxBytes, sw.truncated, sw.presized = 0, 0, false, false
	writers.Put(sw)
}

// Header returns the response header map
func (sw *CaptureResponseWriter) Header() http.Header {
	return sw.header
}

// WriteHeader sets the status code and, when Content-Length is set and the
// body has not yet been grown, presizes body to skip bytes.Buffer's
// doubling-copies on large upstream responses. Bounded by maxBytes so a
// misreported huge CL cannot blow the cap.
func (sw *CaptureResponseWriter) WriteHeader(code int) {
	if code == 0 {
		code = http.StatusOK
	}
	sw.statusCode = code
	if sw.presized {
		return
	}
	sw.presized = true
	n, err := strconv.Atoi(sw.header.Get(headers.NameContentLength))
	if err != nil || n <= 0 {
		return
	}
	if sw.maxBytes > 0 && n > sw.maxBytes {
		n = sw.maxBytes
	}
	sw.body.Grow(n)
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
