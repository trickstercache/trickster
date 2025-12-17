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

// CaptureResponseWriter captures the response body to a byte slice
type CaptureResponseWriter struct {
	http.ResponseWriter
	header     http.Header
	statusCode int
	body       bytes.Buffer
	len        int
}

// NewCaptureResponseWriter returns a new CaptureResponseWriter
func NewCaptureResponseWriter() *CaptureResponseWriter {
	return &CaptureResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
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

// Write appends data to the response body
func (sw *CaptureResponseWriter) Write(b []byte) (int, error) {
	sw.body.Write(b)
	sw.len += len(b)
	return sw.len, nil
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
