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

package blockingquery

import (
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"

	"github.com/stretchr/testify/require"
)

func TestNewCursorDefaultsMinGap(t *testing.T) {
	require.Equal(t, DefaultMinGap, NewCursor(0).minGap)
	require.Equal(t, DefaultMinGap, NewCursor(-time.Second).minGap)
	require.Equal(t, time.Second, NewCursor(time.Second).minGap)
}

// The first request of a subscription must not block, or nothing ever
// arrives to fill the pool.
func TestZeroIndexUntilAServerSuppliesOne(t *testing.T) {
	c := NewCursor(time.Millisecond)
	require.Zero(t, c.Index())

	had, changed := c.Advance("100")
	require.False(t, had, "no cursor was held before the first response")
	require.True(t, changed)
	require.EqualValues(t, 100, c.Index())
}

// An unchanged blocking query is the common case for a stable resource, and
// reporting it lets the caller skip decoding a body it has already applied.
func TestUnchangedIndexIsReported(t *testing.T) {
	c := NewCursor(time.Millisecond)
	c.Advance("100")
	had, changed := c.Advance("100")
	require.True(t, had)
	require.False(t, changed, "an unchanged index must be reported as such")

	had, changed = c.Advance("101")
	require.True(t, had)
	require.True(t, changed)
}

// A backwards index means the server's state was reset. Continuing to block
// on the old cursor would park forever against an index the server will
// never reach again.
func TestBackwardsIndexResetsTheCursor(t *testing.T) {
	c := NewCursor(time.Millisecond)
	c.Advance("100")
	had, changed := c.Advance("5")
	require.True(t, had)
	require.True(t, changed, "a reset must be treated as a change, not a no-op")
	require.Zero(t, c.Index(), "the next request must be a plain read")
}

// Without a usable header there is nothing to block on; claiming a cursor
// the server did not give us would block against a value it never reports.
func TestUnusableHeaderFallsBackToPlainReads(t *testing.T) {
	for name, header := range map[string]string{
		"empty":        "",
		"not a number": "abc",
		"zero":         "0",
		"negative":     "-1",
		"overflow":     "99999999999999999999999999",
	} {
		t.Run(name, func(t *testing.T) {
			c := NewCursor(time.Millisecond)
			c.Advance("100")
			had, changed := c.Advance(header)
			require.False(t, had)
			require.True(t, changed)
			require.Zero(t, c.Index())
		})
	}
}

func TestResetDropsTheCursor(t *testing.T) {
	c := NewCursor(time.Millisecond)
	c.Advance("100")
	require.EqualValues(t, 100, c.Index())
	c.Reset()
	require.Zero(t, c.Index())
}

// Without a cursor there is no server-side wait to rely on, so the caller
// must fall back to its own interval rather than spinning.
func TestNextWaitWithoutCursorDefersToTheInterval(t *testing.T) {
	c := NewCursor(time.Millisecond)
	c.Begin()
	require.Zero(t, c.NextWait())
}

// With a cursor, the waiting happens on the server, so the next request goes
// out immediately -- but not sooner than the floor.
func TestNextWaitHonorsTheFloor(t *testing.T) {
	c := NewCursor(50 * time.Millisecond)
	c.Begin()
	c.Advance("100")

	// a request that returned instantly must be held back
	next := c.NextWait()
	require.Greater(t, next, time.Duration(0))
	require.LessOrEqual(t, next, 50*time.Millisecond)

	// one that already spent longer than the floor re-issues immediately
	c = NewCursor(time.Millisecond)
	c.Begin()
	c.Advance("100")
	time.Sleep(5 * time.Millisecond)
	require.Equal(t, poller.PollNow, c.NextWait())
}

// A resource changing faster than the loop must not become a spin against
// the server at exactly the moment it is busiest.
func TestFastChangingResourceCannotSpin(t *testing.T) {
	const floor = 20 * time.Millisecond
	c := NewCursor(floor)
	index := 100
	var total time.Duration
	for range 10 {
		c.Begin()
		index++
		c.Advance(string(rune('0'+index/100)) + "00")
		// every response arrives instantly, as a flapping service would
		next := c.NextWait()
		require.NotEqual(t, poller.PollNow, next,
			"an instant response must not re-issue immediately")
		total += next
	}
	require.Greater(t, total, 5*floor,
		"ten instant responses should have been spread over several floors")
}

func TestDuration(t *testing.T) {
	require.Equal(t, "5m", Duration(5*time.Minute))
	require.Equal(t, "10m", Duration(10*time.Minute))
	require.Equal(t, "90s", Duration(90*time.Second))
	require.Equal(t, "1s", Duration(time.Second))
}

// The cursor is read by the request decorator and written by the response
// handler, which the poller runs on the same goroutine -- but Stop and
// Begin can race with them, so the state is mutex-guarded.
func TestCursorIsRaceSafe(t *testing.T) {
	c := NewCursor(time.Millisecond)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				switch (i + j) % 4 {
				case 0:
					c.Begin()
				case 1:
					c.Advance("100")
				case 2:
					c.Reset()
				default:
					_ = c.Index()
					_ = c.NextWait()
				}
			}
		})
	}
	wg.Wait()
}
