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

package testutil

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// RecordingTransport sends each request through Inner, or the default transport, and records it
// with its body, so a test can see what reached the origin
type RecordingTransport struct {
	Inner    http.RoundTripper
	mu       sync.Mutex
	requests []RecordedRequest
}

// RecordedRequest is one request a RecordingTransport sent
type RecordedRequest struct {
	Method string
	URL    *url.URL
	Body   []byte
}

// RoundTrip implements http.RoundTripper.
func (t *RecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	u := *r.URL
	t.mu.Lock()
	t.requests = append(t.requests, RecordedRequest{Method: r.Method, URL: &u, Body: body})
	t.mu.Unlock()
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(r)
}

// Take returns the requests recorded since the last call
func (t *RecordingTransport) Take() []RecordedRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.requests
	t.requests = nil
	return out
}
