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

package handlers

import (
	"net/http"
	"sync/atomic"
)

// SwitchHandler is an HTTP Wrapper that allows users to update the underlying handler in-place
// once associated with a net.Listener
type SwitchHandler struct {
	mux       http.Handler
	oldMux    http.Handler
	reloading int32
}

// NewSwitchHandler returns a New *SwitchHandler
func NewSwitchHandler(router http.Handler) *SwitchHandler {
	return &SwitchHandler{mux: router, oldMux: router}
}

// ServeHTTP serves an HTTP Request
func (s *SwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.isReloading() {
		s.oldMux.ServeHTTP(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Update atomically changes the underlying handler without impacting user requests or uptime
func (s *SwitchHandler) Update(h http.Handler) {
	s.oldMux = s.mux
	s.setReloading(true)
	s.mux = h
	s.setReloading(false)
}

// Handler returns the current mux
func (s *SwitchHandler) Handler() http.Handler {
	if s.isReloading() {
		return s.oldMux
	}
	return s.mux
}

func (s *SwitchHandler) isReloading() bool {
	return atomic.LoadInt32(&s.reloading) != 0
}

func (s *SwitchHandler) setReloading(isReloading bool) {
	if isReloading {
		atomic.StoreInt32(&s.reloading, 1)
		return
	}
	atomic.StoreInt32(&s.reloading, 0)
}
