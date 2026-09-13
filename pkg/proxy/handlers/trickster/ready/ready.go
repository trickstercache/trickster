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

// Package ready serves the readiness endpoint, which reports 200 only while every listener is
// serving, any Kubernetes controller has programmed its routes, and the process is not shutting down
package ready

import (
	"net/http"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	// BodyReady is the response body while the process is ready for traffic.
	BodyReady = "ready"
	// BodyNotReady is the response body while a listener is not yet serving.
	BodyNotReady = "not ready"
	// BodyDraining is the response body once a shutdown has begun.
	BodyDraining = "draining"
	// BodyNotProgrammed is the response body while a Kubernetes controller has yet to apply
	// its first translation, so the routes it will serve are not yet in place.
	BodyNotProgrammed = "not programmed"
)

// State records whether the process has begun shutting down, and whether it is still
// waiting on a Kubernetes controller to program its first routes.
type State struct {
	draining atomic.Bool
	pending  atomic.Bool
}

// SetPending marks the process as waiting for a controller's first translation; readiness
// stays false until SetProgrammed.
func (s *State) SetPending() {
	if s != nil {
		s.pending.Store(true)
	}
}

// SetProgrammed clears the wait a SetPending began.
func (s *State) SetProgrammed() {
	if s != nil {
		s.pending.Store(false)
	}
}

// Pending reports whether a controller's first translation is still awaited.
func (s *State) Pending() bool {
	return s != nil && s.pending.Load()
}

// SetDraining marks the process as shutting down; readiness stays false thereafter.
func (s *State) SetDraining() {
	if s != nil {
		s.draining.Store(true)
	}
}

// Draining reports whether a shutdown has begun.
func (s *State) Draining() bool {
	return s != nil && s.draining.Load()
}

// Listeners reports whether every listener is accepting connections.
type Listeners interface {
	Serving() bool
}

// HandlerFunc responds 200 while listeners are serving, no controller is still programming its
// first routes and no shutdown has begun, and 503 otherwise, so load balancers route only then.
func HandlerFunc(state *State, listeners Listeners) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		switch {
		case state.Draining():
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(BodyDraining))
		case listeners == nil || !listeners.Serving():
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(BodyNotReady))
		case state.Pending():
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(BodyNotProgrammed))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(BodyReady))
		}
	}
}
