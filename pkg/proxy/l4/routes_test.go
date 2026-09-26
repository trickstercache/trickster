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
package l4

import (
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

// rotating is a test upstream that commits flows to its addresses in turn and keeps what the
// relay reports of each. An empty or undialable address holds its turn and refuses it.
type rotating struct {
	addrs []string
	pos   atomic.Uint64
	// retries is how many more routes a flow may be offered after a failed dial
	retries int
	final   bool

	mu     sync.Mutex
	flows  []Flow
	routes []*recordedRoute
}

type recordedRoute struct {
	addr      string
	final     bool
	attempt   int
	dialed    atomic.Int32
	dialErr   atomic.Pointer[error]
	dialTook  atomic.Int64
	firstByte atomic.Int32
	closed    atomic.Int32
	closeErr  atomic.Pointer[error]
}

func rotate(addrs ...string) *rotating {
	return &rotating{addrs: addrs}
}

func (u *rotating) Pick(f Flow) (Route, bool) {
	u.mu.Lock()
	u.flows = append(u.flows, f)
	u.mu.Unlock()
	if len(u.addrs) == 0 {
		return nil, false
	}
	addr := u.addrs[u.pos.Add(1)%uint64(len(u.addrs))]
	if addr == "" || Refusing(addr) {
		return nil, false
	}
	r := &recordedRoute{addr: addr, final: u.final}
	u.mu.Lock()
	u.routes = append(u.routes, r)
	u.mu.Unlock()
	return r, true
}

// retrying is a rotating upstream that offers the next address when a dial fails
type retrying struct{ *rotating }

func (u retrying) Retry(f Flow, failed Route) (Route, bool) {
	prev := failed.(*recordedRoute)
	if prev.attempt >= u.retries {
		return nil, false
	}
	next, ok := u.Pick(f)
	if ok {
		next.(*recordedRoute).attempt = prev.attempt + 1
	}
	return next, ok
}

func (u *rotating) seen() ([]Flow, []*recordedRoute) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Flow(nil), u.flows...), append([]*recordedRoute(nil), u.routes...)
}

func (r *recordedRoute) Addr() string { return r.addr }

func (r *recordedRoute) Final() bool { return r.final }

func (r *recordedRoute) Dialed(d time.Duration, err error) {
	r.dialed.Add(1)
	r.dialTook.Store(int64(d))
	if err != nil {
		r.dialErr.Store(&err)
	}
}

func (r *recordedRoute) FirstByte() { r.firstByte.Add(1) }

func (r *recordedRoute) Closed(err error) {
	r.closed.Add(1)
	if err != nil {
		r.closeErr.Store(&err)
	}
}

func (r *recordedRoute) failed() bool { return r.dialErr.Load() != nil }

// countingObserver keeps what a relay reports of itself
type countingObserver struct {
	mu      sync.Mutex
	active  int
	results map[string]int
	drops   map[string]int
	bytes   map[string]int64
}

func (o *countingObserver) add(m *map[string]int, key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if *m == nil {
		*m = make(map[string]int)
	}
	(*m)[key]++
}

func (o *countingObserver) Opened() {
	o.mu.Lock()
	o.active++
	o.mu.Unlock()
}

func (o *countingObserver) Ended() {
	o.mu.Lock()
	o.active--
	o.mu.Unlock()
}

func (o *countingObserver) Result(r string) { o.add(&o.results, r) }

func (o *countingObserver) Dropped(reason string) { o.add(&o.drops, reason) }

func (o *countingObserver) Bytes(direction string, n int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bytes == nil {
		o.bytes = make(map[string]int64)
	}
	o.bytes[direction] += n
}

func (o *countingObserver) dropped(reason string) float64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return float64(o.drops[reason])
}

func (o *countingObserver) snapshot() (active int, results map[string]int, bytes map[string]int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	results = make(map[string]int, len(o.results))
	maps.Copy(results, o.results)
	bytes = make(map[string]int64, len(o.bytes))
	maps.Copy(bytes, o.bytes)
	return o.active, results, bytes
}
