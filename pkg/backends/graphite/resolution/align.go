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

// AlignInterval reproduces whisper's __archive_fetch rounding: both edges
// become (t - t%step) + step, zero-length widened by one step; range is [start, end).
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

// Clamp applies whisper's range checks: from > now or until < now-maxRetention
// yields no data; from and until are clamped. maxRetention 0 clamps only until.
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

// BucketPhase is zero: whisper timestamps are epoch-aligned multiples of the
// step, and AlignInterval's +step shifts coverage, not bucket boundaries.
const BucketPhase = time.Duration(0)

// RequestWindow is the inverse of AlignInterval: the from/until to send so the
// origin returns exactly the step-aligned buckets in [start, end] inclusive.
func RequestWindow(start, end time.Time, step time.Duration) (time.Time, time.Time) {
	return start.Add(-step), end
}
