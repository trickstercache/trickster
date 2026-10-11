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

package ratelimit

import (
	"maps"
	"strings"
	"sync"
	"time"
)

// Shape is the part of a limiter that decides whether a reload keeps its buckets.
type Shape struct {
	// Keys is a stable encoding of the key list, in order. Empty means one bucket for every event.
	Keys       string
	IPv6Prefix int
	Window     time.Duration
	Limit      uint32
	MaxKeys    int
}

// Policy is applied on each lookup and does not by itself discard buckets.
type Policy struct {
	Missing MissingKey
	OnFull  MaxKeysAction
}

// EncodeKeys joins key spellings so two lists compare equal only in the same order.
func EncodeKeys(keys []string) string {
	return strings.Join(keys, "\x00")
}

type kept struct {
	shape Shape
	store *tableStore
}

var registry = struct {
	mu       sync.Mutex
	updateMu sync.Mutex
	byName   map[string]*kept
	update   *RegistryUpdate
}{byName: map[string]*kept{}}

// Lookup reuses counts when shape is unchanged and applies policy on every call.
// During an update, replacements are staged until the configuration commits.
func Lookup(name string, shape Shape, policy Policy) *Limiter {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	k := registry.byName[name]
	if k == nil || k.shape != shape {
		dst := registry.byName
		if registry.update != nil {
			dst = registry.update.byName
			k = dst[name]
		}
		if k == nil || k.shape != shape {
			k = &kept{shape: shape, store: newStore(shape.Limit, shape.Window, shape.MaxKeys)}
			dst[name] = k
		}
	}
	return &Limiter{
		store:     k.store,
		limit:     shape.Limit,
		window:    shape.Window,
		missing:   policy.Missing,
		onFull:    policy.OnFull,
		oneBucket: shape.Keys == "",
	}
}

// ForgetExcept drops kept stores for which keep returns false. A failed load must not call it:
// until a configuration is applied, the previous stores stay selectable.
func ForgetExcept(keep func(name string) bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	forgetExcept(keep)
}

func forgetExcept(keep func(name string) bool) {
	for name := range registry.byName {
		if keep != nil && keep(name) {
			continue
		}
		delete(registry.byName, name)
	}
}

// RegistryUpdate stages replacement stores while a configuration is built.
// Aborting leaves the committed stores, including their ongoing counts, intact.
type RegistryUpdate struct {
	byName map[string]*kept
}

// BeginUpdate serializes configuration updates. The caller must commit or abort the result.
func BeginUpdate() *RegistryUpdate {
	registry.updateMu.Lock()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	u := &RegistryUpdate{byName: make(map[string]*kept)}
	registry.update = u
	return u
}

// Commit publishes the staged stores and drops names the applied configuration no longer keeps.
func (u *RegistryUpdate) Commit(keep func(name string) bool) {
	u.finish(keep, true)
}

// Abort discards staged stores. Calling it after Commit or Abort does nothing.
func (u *RegistryUpdate) Abort() {
	u.finish(nil, false)
}

func (u *RegistryUpdate) finish(keep func(name string) bool, commit bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if u == nil || registry.update != u {
		return
	}
	if commit {
		maps.Copy(registry.byName, u.byName)
		forgetExcept(keep)
	}
	registry.update = nil
	registry.updateMu.Unlock()
}

// Walk calls fn with each kept limiter's name and how many buckets it holds.
func Walk(fn func(name string, keys int)) {
	if fn == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for name, k := range registry.byName {
		n := 0
		if k != nil && k.store != nil {
			n = k.store.Len()
		}
		fn(name, n)
	}
}
