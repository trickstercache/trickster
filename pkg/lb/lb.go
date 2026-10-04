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
// Package lb is a protocol-neutral load-balancing core: pool membership, health-filtered
// immutable snapshots and per-member runtime stats. It imports only the standard library.
package lb

// Health reports a member's current health status. Higher is healthier; a pool compares the
// value against its floor. A member without a Health is always eligible.
type Health interface {
	Get() int32
}

// Notifier is optionally implemented by a Health that can announce its transitions, which lets
// a pool rebuild its snapshot inside the transition instead of waiting for Refresh.
type Notifier interface {
	// OnChange registers fn to be called, synchronously and outside any lock the Notifier
	// holds, after each status change with the previous and the new status.
	OnChange(fn func(prev, next int32)) Subscription
}

// Subscription ends a Notifier registration.
type Subscription interface {
	// Unsubscribe never blocks on a callback that is already running, so it may be called
	// from inside one; a callback that has already been captured may still run once.
	Unsubscribe()
}
