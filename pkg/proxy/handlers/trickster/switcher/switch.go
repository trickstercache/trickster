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

package switcher

import (
	"net/http"
	"sync/atomic"
)

// SwitchHandler is an HTTP Wrapper that allows users to update the underlying handler in-place
// once associated with a net.Listener
type SwitchHandler struct {
	router atomic.Pointer[http.Handler]
}

// NewSwitchHandler returns a New *SwitchHandler
func NewSwitchHandler(router http.Handler) *SwitchHandler {
	s := &SwitchHandler{}
	s.router.Store(&router)
	return s
}

// ServeHTTP serves an HTTP Request
func (s *SwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.load(); h != nil {
		h.ServeHTTP(w, r)
		return
	}
	http.Error(w, "service unavailable", http.StatusServiceUnavailable)
}

// Update atomically changes the underlying handler without impacting user requests or uptime
func (s *SwitchHandler) Update(h http.Handler) {
	// swap is non-blocking by design; do not add an in-flight drain here, it reintroduces the deadlock the atomic.Pointer fixed
	s.router.Store(&h)
}

// Handler returns the current router
func (s *SwitchHandler) Handler() http.Handler {
	return s.load()
}

// load returns the currently published handler, or nil if none has been set.
func (s *SwitchHandler) load() http.Handler {
	if p := s.router.Load(); p != nil {
		return *p
	}
	return nil
}
