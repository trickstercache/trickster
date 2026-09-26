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

package stepwindow

import (
	"testing"
	"time"
)

func TestSame(t *testing.T) {
	const step = time.Minute
	cases := []struct {
		a, b     time.Time
		expected bool
	}{
		{time.Unix(120, 0), time.Unix(179, 999_999_999), true},
		{time.Unix(179, 999_999_999), time.Unix(180, 0), false},
		{time.Unix(-1, 0), time.Unix(-60, 0), true},
		{time.Unix(-1, 0), time.Unix(0, 0), false},
		{time.Unix(-60, 0), time.Unix(-61, 0), false},
	}
	for _, c := range cases {
		if got := Same(c.a, c.b, step); got != c.expected {
			t.Errorf("Same(%d, %d): expected %t got %t", c.a.UnixNano(), c.b.UnixNano(), c.expected, got)
		}
	}
	// windows align to the Unix epoch, not to Go's zero time
	if Same(time.Unix(6, 0), time.Unix(7, 0), 7*time.Second) {
		t.Error("expected 6s and 7s to be in different 7s windows")
	}
	if Same(time.Unix(1, 0), time.Unix(1, 0), 0) {
		t.Error("expected a non-positive step to never match")
	}
}

func TestRetry(t *testing.T) {
	calls := 0
	got, ok := Retry(time.Hour, 3, func(attempt int, _ time.Time) int {
		calls++
		return attempt
	})
	if !ok || got != 0 || calls != 1 {
		t.Errorf("expected one successful call, got result %d ok %t calls %d", got, ok, calls)
	}

	// a nanosecond step is crossed by every attempt, so every retry is used
	calls = 0
	got, ok = Retry(time.Nanosecond, 3, func(attempt int, _ time.Time) int {
		calls++
		time.Sleep(time.Microsecond)
		return attempt
	})
	if ok || got != 2 || calls != 3 {
		t.Errorf("expected three failed attempts, got result %d ok %t calls %d", got, ok, calls)
	}
}
