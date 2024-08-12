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

// package writer represents a Test ResponseWriter for use in Unit Tests
package writer

import "net/http"

func NewWriter() http.ResponseWriter {
	return &TestResponseWriter{
		Headers: make(http.Header),
		Bytes:   make([]byte, 0, 8192),
	}
}

type TestResponseWriter struct {
	Headers    http.Header
	StatusCode int
	Bytes      []byte
}

func (w *TestResponseWriter) Header() http.Header {
	return w.Headers
}

func (w *TestResponseWriter) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
}

func (w *TestResponseWriter) Write(b []byte) (int, error) {
	w.Bytes = append(w.Bytes, b...)
	return len(b), nil
}

func (w *TestResponseWriter) Reset() {
	w.Headers = make(http.Header)
	w.StatusCode = 0
	w.Bytes = make([]byte, 0, 8192)
}
