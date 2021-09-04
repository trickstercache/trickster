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
		observer := &responseObserver{
			w,
			"unknown",
			0,
		}

		n := time.Now()
		next.ServeHTTP(observer, r)

		metrics.FrontendRequestDuration.WithLabelValues(backendName, backendProvider,
			r.Method, path, observer.status).Observe(time.Since(n).Seconds())
		metrics.FrontendRequestStatus.WithLabelValues(backendName, backendProvider,
			r.Method, path, observer.status).Inc()
		metrics.FrontendRequestWrittenBytes.WithLabelValues(backendName, backendProvider,
			r.Method, path, observer.status).Add(observer.bytesWritten)
	})
}

type responseObserver struct {
	http.ResponseWriter

	status       string
	bytesWritten float64
}

func (w *responseObserver) WriteHeader(statusCode int) {
	w.ResponseWriter.WriteHeader(statusCode)
	switch {
	case statusCode >= 100 && statusCode < 199:
		w.status = "1xx"
	case statusCode >= 200 && statusCode < 299:
		w.status = "2xx"
	case statusCode >= 300 && statusCode < 399:
		w.status = "3xx"
	case statusCode >= 400 && statusCode < 499:
		w.status = "4xx"
	case statusCode >= 500 && statusCode < 599:
		w.status = "5xx"
	}
}

func (w *responseObserver) Write(b []byte) (int, error) {
	bytesWritten, err := w.ResponseWriter.Write(b)

	w.bytesWritten += float64(bytesWritten)

	return bytesWritten, err
}
