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

package middleware

import (
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// Decorate decorates a function in such a way that it captures both the
// returned status and the time used to execute a request from the front end
// perspective
func Decorate(backendName, backendProvider, path string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observer, ok := w.(*ResponseObserver)
		if !ok {
			observer = NewResponseObserver(w)
		}
		startBytes := observer.BytesWritten()

		n := time.Now()
		next.ServeHTTP(observer, r)
		statusClass := observer.StatusClass()

		metrics.FrontendRequestDuration.WithLabelValues(backendName, backendProvider,
			r.Method, path, statusClass).Observe(time.Since(n).Seconds())
		metrics.FrontendRequestStatus.WithLabelValues(backendName, backendProvider,
			r.Method, path, statusClass).Inc()
		metrics.FrontendRequestWrittenBytes.WithLabelValues(backendName, backendProvider,
			r.Method, path, statusClass).Add(float64(observer.BytesWritten() - startBytes))
	})
}

// ResponseObserver records the response status and number of bytes written.
type ResponseObserver struct {
	http.ResponseWriter

	statusCode   int
	bytesWritten int64
	wroteHeader  bool
}

// NewResponseObserver wraps w with a response observer.
func NewResponseObserver(w http.ResponseWriter) *ResponseObserver {
	return &ResponseObserver{ResponseWriter: w, statusCode: http.StatusOK}
}

func (w *ResponseObserver) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(statusCode)
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
	w.statusCode = statusCode
}

func (w *ResponseObserver) Write(b []byte) (int, error) {
	w.wroteHeader = true
	bytesWritten, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(bytesWritten)
	return bytesWritten, err
}

// StatusCode returns the first HTTP status written, or 200 if none was written.
func (w *ResponseObserver) StatusCode() int { return w.statusCode }

// StatusClass returns the recorded HTTP status class, such as "2xx".
func (w *ResponseObserver) StatusClass() string {
	class := w.statusCode / 100
	if class >= 0 && class < len(statusClasses) {
		return statusClasses[class]
	}
	return statusClasses[0]
}

// BytesWritten returns the number of response body bytes written.
func (w *ResponseObserver) BytesWritten() int64 { return w.bytesWritten }

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *ResponseObserver) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var statusClasses = [6]string{"0xx", "1xx", "2xx", "3xx", "4xx", "5xx"}
