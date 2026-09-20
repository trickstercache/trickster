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

import (
	"math"
	"sync/atomic"
	"time"
)

// Stats is a member's runtime state. It is held by pointer so it can outlive the Member that
// carries it, such as when a member is rebuilt with a new weight.
type Stats struct {
	inflight atomic.Int64
	// float64 bits of an average in nanoseconds; last writer wins
	latency atomic.Uint64
	// unix nanosecond time of the last latency sample
	stamp atomic.Int64
	// consecutive failed outcomes
	fails atomic.Int32
}

// Inflight returns the units of work currently committed to the member.
func (s *Stats) Inflight() int64 {
	return s.inflight.Load()
}

// Latency returns the member's latency average, or 0 when it has no sample.
func (s *Stats) Latency() time.Duration {
	return time.Duration(math.Float64frombits(s.latency.Load()))
}

// LastSample returns when the latency average was last updated; zero when never.
func (s *Stats) LastSample() time.Time {
	ns := s.stamp.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Failures returns the member's count of consecutive failed outcomes.
func (s *Stats) Failures() int32 {
	return s.fails.Load()
}

// Faded returns the latency average as it stands at now, having faded toward zero since the
// last sample: close to exponentially with time constant decay, at the cost of one division.
// A member that is given no work gets no fresh sample, so this is how one ranked behind its
// peers, on an old sample or a penalty, is eventually tried again.
func (s *Stats) Faded(now time.Time, decay time.Duration) time.Duration {
	avg := math.Float64frombits(s.latency.Load())
	elapsed := now.UnixNano() - s.stamp.Load()
	if avg == 0 || elapsed <= 0 || decay <= 0 {
		return time.Duration(avg)
	}
	// the reciprocal of e^x's cubic expansion: positive, falling and continuous for x >= 0
	x := float64(elapsed) / float64(decay)
	return time.Duration(avg / (1 + x*(1+x*(0.5+x/6))))
}

// observe folds a latency sample, in nanoseconds, into a peak average: a sample at or above
// the average replaces it at once; one below pulls it down, further the older the average is.
// Concurrent samples may overwrite one another, which a load signal tolerates.
func (s *Stats) observe(sample float64, now int64, decay float64) {
	stamp := s.stamp.Load()
	avg := math.Float64frombits(s.latency.Load())
	if stamp != 0 && sample < avg {
		elapsed := float64(max(now-stamp, 0))
		sample += (avg - sample) * math.Exp(-elapsed/decay)
	}
	s.latency.Store(math.Float64bits(sample))
	s.stamp.Store(now)
}
