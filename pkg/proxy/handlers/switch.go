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
	router    http.Handler
	oldRouter http.Handler
	reloading atomic.Int32
}

// NewSwitchHandler returns a New *SwitchHandler
func NewSwitchHandler(router http.Handler) *SwitchHandler {
	return &SwitchHandler{router: router, oldRouter: router}
}

// ServeHTTP serves an HTTP Request
func (s *SwitchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.isReloading() {
		s.oldRouter.ServeHTTP(w, r)
		return
	}
	s.router.ServeHTTP(w, r)
}

// Update atomically changes the underlying handler without impacting user requests or uptime
func (s *SwitchHandler) Update(h http.Handler) {
	s.oldRouter = s.router
	s.setReloading(true)
	s.router = h
	s.setReloading(false)
}

// Handler returns the current router
func (s *SwitchHandler) Handler() http.Handler {
	if s.isReloading() {
		return s.oldRouter
	}
	return s.router
}

func (s *SwitchHandler) isReloading() bool {
	return s.reloading.Load() != 0
}

func (s *SwitchHandler) setReloading(isReloading bool) {
	if isReloading {
		s.reloading.Store(1)
		return
	}
	s.reloading.Store(0)
}
