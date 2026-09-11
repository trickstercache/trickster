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

package discovery

import "sync"

// Emitter delivers membership snapshots to a handler with the provider
// contract applied uniformly: snapshots are canonicalized, no-change
// emissions are suppressed, deliveries are serialized (concurrent Emit
// calls cannot reach the handler out of order), and nothing is delivered
// after Stop. Handlers must not call back into the Emitter.
type Emitter struct {
	handler SnapshotHandler

	// mtx serializes the compare-update-deliver sequence end to end
	mtx     sync.Mutex
	last    Snapshot
	hasLast bool
	stopped bool
}

// NewEmitter returns an Emitter delivering to handler
func NewEmitter(handler SnapshotHandler) *Emitter {
	return &Emitter{handler: handler}
}

// Emit canonicalizes s and delivers it when membership differs from the
// previously delivered snapshot
func (e *Emitter) Emit(s Snapshot) {
	canonical := s.Canonical()
	e.mtx.Lock()
	defer e.mtx.Unlock()
	if e.stopped || (e.hasLast && canonical.Equal(e.last)) {
		return
	}
	e.last = canonical
	e.hasLast = true
	e.handler(canonical)
}

// Stop suppresses all further deliveries
func (e *Emitter) Stop() {
	e.mtx.Lock()
	e.stopped = true
	e.mtx.Unlock()
}
