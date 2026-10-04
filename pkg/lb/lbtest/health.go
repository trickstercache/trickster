/*
 * Copyright 2026 The Trickster Authors
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
package lbtest

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Health is a settable lb.Health that announces its transitions the way lb.Notifier asks:
// synchronously, outside its own lock, with an Unsubscribe that never waits.
type Health struct {
	status atomic.Int32
	mtx    sync.Mutex
	subs   []*subscription
}

type subscription struct {
	health *Health
	fn     func(prev, next int32)
	dead   atomic.Bool
}

// NewHealth returns a Health at the provided status.
func NewHealth(status int32) *Health {
	h := &Health{}
	h.status.Store(status)
	return h
}

// Get returns the current status.
func (h *Health) Get() int32 {
	return h.status.Load()
}

// Set stores a status and runs the registered callbacks when it is a change.
func (h *Health) Set(next int32) {
	prev := h.status.Swap(next)
	if prev == next {
		return
	}
	h.mtx.Lock()
	subs := h.subs
	h.mtx.Unlock()
	for _, s := range subs {
		if !s.dead.Load() {
			s.fn(prev, next)
		}
	}
}

// OnChange registers fn to run after each change of status.
func (h *Health) OnChange(fn func(prev, next int32)) lb.Subscription {
	s := &subscription{health: h, fn: fn}
	h.mtx.Lock()
	defer h.mtx.Unlock()
	h.subs = append(slices.Clone(h.subs), s)
	return s
}

func (s *subscription) Unsubscribe() {
	if s.dead.Swap(true) {
		return
	}
	h := s.health
	h.mtx.Lock()
	defer h.mtx.Unlock()
	h.subs = slices.DeleteFunc(slices.Clone(h.subs), func(o *subscription) bool { return o == s })
}
