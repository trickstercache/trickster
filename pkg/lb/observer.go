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

// EventKind identifies what an Event reports.
type EventKind uint8

const (
	// EventSnapshot reports that a pool published a new snapshot.
	EventSnapshot EventKind = iota + 1
	// EventPanic reports a panic recovered while rebuilding a snapshot.
	EventPanic
)

// Event is one notification to an Observer. Fields beyond Kind are set as the kind requires.
type Event struct {
	Kind EventKind
	// Gen, Eligible and Configured describe the snapshot of an EventSnapshot
	Gen        uint64
	Eligible   int
	Configured int
	// Panic and Stack carry the recovered value and stack of an EventPanic
	Panic any
	Stack []byte
}

// Observer receives events from the core, which itself neither logs nor meters. Observe is
// never called on a selection path, and must not call back into the pool that invoked it.
type Observer interface {
	Observe(Event)
}
