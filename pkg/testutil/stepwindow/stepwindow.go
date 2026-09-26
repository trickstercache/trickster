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

// Package stepwindow keeps time-sensitive range query tests from straddling a
// step boundary between the test's clock read and Trickster's.
package stepwindow

import "time"

// Same reports whether a and b fall in the same step-aligned window of Unix time,
// which is how Trickster normalizes the end of a range query and its own now.
func Same(a, b time.Time, step time.Duration) bool {
	return step > 0 && window(a, step) == window(b, step)
}

// Retry calls fn with the current time, and calls it again, up to attempts times,
// when a step boundary passed before fn returned. The boundary is behind every
// retry, so a retry only fails if fn runs for a full step. It returns the last
// result and whether that attempt stayed within the window it started in.
func Retry[T any](step time.Duration, attempts int, fn func(attempt int, now time.Time) T) (T, bool) {
	var result T
	for i := range attempts {
		now := time.Now()
		result = fn(i, now)
		if Same(now, time.Now(), step) {
			return result, true
		}
	}
	return result, false
}

func window(t time.Time, step time.Duration) int64 {
	ns, s := t.UnixNano(), int64(step)
	w := ns / s
	if ns < 0 && ns%s != 0 {
		w--
	}
	return w
}
