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

package resolution

import "time"

// AlignInterval reproduces whisper's __archive_fetch rounding (design note
// §2.2): both edges become (t - t%step) + step, and a zero-length result is
// widened by one step. The returned range is half-open: the origin returns
// the points with timestamps in [start, end).
func AlignInterval(from, until time.Time, step time.Duration) (time.Time, time.Time) {
	s := floorStep(from, step).Add(step)
	e := floorStep(until, step).Add(step)
	if !e.After(s) {
		e = s.Add(step)
	}
	return s, e
}

func floorStep(t time.Time, step time.Duration) time.Time {
	sec := t.Unix()
	st := int64(step / time.Second)
	if st <= 0 {
		return t
	}
	f := sec - ((sec%st)+st)%st
	return time.Unix(f, 0)
}

// Clamp applies whisper's range checks in order (design note §2.3):
// from > now or until < now-maxRetention yields no data; from is raised to
// now-maxRetention and until lowered to now. A maxRetention of 0 means
// unknown and only the until clamp is applied.
func Clamp(from, until, now time.Time, maxRetention time.Duration) (time.Time, time.Time, bool) {
	if from.After(now) {
		return from, until, false
	}
	if maxRetention > 0 {
		oldest := now.Add(-maxRetention)
		if until.Before(oldest) {
			return from, until, false
		}
		if from.Before(oldest) {
			from = oldest
		}
	}
	if until.After(now) {
		until = now
	}
	return from, until, true
}

// BucketPhase is the phase of Graphite's buckets relative to the Unix
// epoch: zero. Whisper timestamps are multiples of the step; the +step in
// AlignInterval shifts which buckets a request covers, not where their
// boundaries lie, so TimeRangeQuery.Phase stays 0 and the shift is applied
// when the upstream request window is built from an extent.
const BucketPhase = time.Duration(0)

// RequestWindow is the inverse of AlignInterval: the from/until to send so
// that the origin returns exactly the buckets in [start, end] inclusive of
// both step-aligned edges. from must be strictly before the first bucket
// (the +step rounding), and until must land in the last bucket.
func RequestWindow(start, end time.Time, step time.Duration) (time.Time, time.Time) {
	return start.Add(-step), end
}
