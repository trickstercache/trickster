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

// Package blockingquery implements the cursor half of HashiCorp's blocking
// query protocol, which Consul and Nomad share.
//
// A blocking query carries the index of the state the client last saw. The
// server parks the request until its own index moves past that, or until
// the client's wait elapses, and returns the current index in a response
// header. Repeating that turns polling into an event stream: a change is
// observed within a round trip, and an unchanged resource costs one parked
// connection per wait rather than a request per interval.
//
// The protocol has three traps, and this package exists so that no provider
// has to rediscover them:
//
//   - An index that goes backwards means the server's state was reset -- a
//     restart, or a resource deleted and recreated. A client that keeps
//     blocking on the old cursor parks forever against an index the server
//     will never reach again, so it must start over.
//   - An index below 1 is reserved and must not be used as a cursor.
//   - A resource that changes faster than the loop turns "the server does
//     the waiting" into a spin against that server at exactly the moment it
//     is busiest, so successive requests need a floor between them.
package blockingquery

import (
	"strconv"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"
)

// DefaultMinGap is the shortest time between successive blocking queries
// applied when NewCursor is given no floor of its own.
const DefaultMinGap = 100 * time.Millisecond

// Cursor tracks one subscription's blocking-query index. Its zero value is
// not usable; construct one with NewCursor.
type Cursor struct {
	minGap time.Duration

	mtx sync.Mutex
	// index is the last index the server reported. Zero means the next
	// request is a plain read rather than a blocking one, which is both the
	// initial state and the recovery state.
	index uint64
	// startedAt is when the in-flight request began, used to hold
	// successive requests apart
	startedAt time.Time
}

// NewCursor returns a Cursor enforcing minGap between requests; a
// non-positive minGap selects DefaultMinGap.
func NewCursor(minGap time.Duration) *Cursor {
	if minGap <= 0 {
		minGap = DefaultMinGap
	}
	return &Cursor{minGap: minGap}
}

// Begin records the start of a request, for the inter-request floor.
func (c *Cursor) Begin() {
	c.mtx.Lock()
	c.startedAt = time.Now()
	c.mtx.Unlock()
}

// Index returns the cursor to send, or zero when the next request should be
// a plain, immediately-returning read.
func (c *Cursor) Index() uint64 {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.index
}

// Reset drops the cursor, so the next request is a plain read.
//
// Callers should reset after a failed request: the failure may itself be
// because the cursor is no longer meaningful, and a plain read always
// yields a usable answer where a blocking query on a stale index may not.
func (c *Cursor) Reset() {
	c.mtx.Lock()
	c.index = 0
	c.mtx.Unlock()
}

// Advance records the index from a response header, reporting whether a
// cursor was already held and whether it changed.
//
// When had is true and changed is false, the request was a blocking query
// that timed out with the resource unchanged: the body is identical to the
// one already applied, and the caller can skip decoding it entirely. That
// saving is what makes a long wait cheap.
func (c *Cursor) Advance(header string) (had, changed bool) {
	next, err := strconv.ParseUint(header, 10, 64)
	if err != nil || next == 0 {
		// no usable cursor: fall back to plain reads rather than blocking
		// against a value the server did not give us
		c.Reset()
		return false, true
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	prev := c.index
	if next < prev {
		// the server's state was reset; start over rather than block on an
		// index it will never reach again
		c.index = 0
		return prev > 0, true
	}
	c.index = next
	return prev > 0, next != prev
}

// NextWait returns how long to pause before the next request, in the form
// poller.Source expects: poller.PollNow to re-issue immediately, zero to
// defer to the poller's configured interval, or a positive duration.
//
// Once a cursor is held the answer is normally "immediately", because the
// waiting happens on the server; the floor keeps a fast-changing resource
// from turning that into a spin.
func (c *Cursor) NextWait() time.Duration {
	c.mtx.Lock()
	index, startedAt := c.index, c.startedAt
	c.mtx.Unlock()
	if index == 0 {
		// nothing to block on; fall back to the configured interval
		return 0
	}
	if elapsed := time.Since(startedAt); elapsed < c.minGap {
		return c.minGap - elapsed
	}
	return poller.PollNow
}

// Duration renders a wait in the form the HashiCorp APIs accept, which
// permits only whole seconds or minutes.
func Duration(d time.Duration) string {
	if d%time.Minute == 0 {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}
