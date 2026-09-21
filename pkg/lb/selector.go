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
package lb

// Needs declares what a selector consumes, so that no caller does work for a selector that
// would ignore it: a selector with no needs costs a pick nothing beyond its own Select.
type Needs uint8

const (
	// NeedKey asks for Flow.Key, as affinity strategies do.
	NeedKey Needs = 1 << iota
	// NeedInflight asks for each member's in-flight count to be kept.
	NeedInflight
	// NeedLatency asks for each member's latency average to be kept.
	NeedLatency
)

// Has reports whether every need in want is declared.
func (n Needs) Has(want Needs) bool {
	return n&want == want
}

// Flow is the unit of work being balanced: a request, a connection, a session. It is passed
// by value, and is a struct so it can grow without changing Selector.
type Flow struct {
	// Key is a hash of whatever identifies the flow for affinity; stable across processes
	Key uint64
	// HasKey is false when the caller had nothing to derive a key from
	HasKey bool
}

// Selector is a load-balancing strategy. An instance holds the strategy's own state, such as
// a rotation counter, and belongs to one Balancer.
type Selector interface {
	// Name identifies the strategy.
	Name() string
	// Needs declares what the strategy consumes.
	Needs() Needs
	// Prepare runs off the selection path, once per snapshot, and returns the strategy's
	// precomputed form of it. It is only called with a snapshot that has members.
	Prepare(*Snapshot) Prepared
}

// Prepared is a strategy bound to one immutable snapshot.
type Prepared interface {
	// Select returns a member of the snapshot it was prepared from. It is the selection
	// path: it must not lock, block or allocate, and must finish in a bounded number of steps.
	Select(Flow) *Member
}

// Picker is what every pick-one mechanism presents to every plane.
type Picker interface {
	// Needs is the Needs of the strategy behind the picker.
	Needs() Needs
	// Pick commits one flow to a member, or returns false when no member is eligible.
	Pick(Flow) (Pick, bool)
}

// PickerProvider is implemented by a member payload that is itself balanced, such as a pool
// whose members are pools. It returns nil when the payload does not pick one member per flow.
type PickerProvider interface {
	Picker() Picker
}

// Outcome is how a committed flow ended, as far as its member is concerned.
type Outcome uint8

const (
	// OutcomeOK means the member did the work.
	OutcomeOK Outcome = iota
	// OutcomeFailed means the member was reached and then failed or answered badly.
	OutcomeFailed
	// OutcomeConnectFailed means the member could not be reached at all.
	OutcomeConnectFailed
	// OutcomeCanceled means the caller gave up first, which says nothing about the member.
	OutcomeCanceled
)
