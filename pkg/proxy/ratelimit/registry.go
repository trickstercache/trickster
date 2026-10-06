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
	mu     sync.Mutex
	byName map[string]*kept
}{byName: map[string]*kept{}}

// Lookup returns a limiter for name. The store is the one already kept when shape is unchanged,
// and a new one otherwise. policy is always this call's, including after a reload.
func Lookup(name string, shape Shape, policy Policy) *Limiter {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	k := registry.byName[name]
	if k == nil || k.shape != shape {
		k = &kept{
			shape: shape,
			store: newStore(shape.Limit, shape.Window, shape.MaxKeys),
		}
		registry.byName[name] = k
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
	for name := range registry.byName {
		if keep != nil && keep(name) {
			continue
		}
		delete(registry.byName, name)
	}
}

func resetRegistry() {
	registry.mu.Lock()
	registry.byName = map[string]*kept{}
	registry.mu.Unlock()
}
