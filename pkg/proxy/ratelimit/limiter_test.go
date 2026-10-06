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

package ratelimit

import (
	"testing"
	"time"
)

func TestMissingKeyExemptSkipsTheBucket(t *testing.T) {
	l := New(Config{Limit: 1, Window: time.Second, MaxKeys: 10, Missing: MissingExempt})
	for range 5 {
		d := l.Take(0, false, 0, 1)
		if !d.Allowed || d.Result != ResultExempt {
			t.Fatalf("exempt: %+v", d)
		}
	}
	if l.Len() != 0 {
		t.Fatalf("exempt events stored %d buckets", l.Len())
	}
	if d := l.Take(4, true, 0, 1); !d.Allowed {
		t.Fatalf("present key: %+v", d)
	}
	if d := l.Take(4, true, 0, 1); d.Allowed {
		t.Fatal("the present key was not counted")
	}
}

func TestMissingKeySharedCountsTogether(t *testing.T) {
	l := New(Config{Limit: 2, Window: time.Second, MaxKeys: 10, Missing: MissingShared})
	if d := l.Take(9, false, 0, 1); !d.Allowed || d.Result != ResultAllowed {
		t.Fatalf("first missing: %+v", d)
	}
	if d := l.Take(9, false, 0, 1); !d.Allowed {
		t.Fatalf("second missing: %+v", d)
	}
	if l.Take(9, false, 0, 1).Allowed {
		t.Fatal("missing keys did not share a bucket")
	}
	if !l.Take(3, false, 0, 1).Allowed {
		t.Fatal("a different partial hash shared the first bucket")
	}
}

func TestEmptyKeysAreOneBucketWhateverThePolicy(t *testing.T) {
	l := New(Config{Limit: 1, Window: time.Second, MaxKeys: 10, Missing: MissingExempt, OneBucket: true})
	if !l.Take(0, false, 0, 1).Allowed {
		t.Fatal("the single bucket refused its first event")
	}
	if l.Take(99, true, 0, 1).Allowed {
		t.Fatal("empty keys counted events apart")
	}
}

func TestFullTableAllowAndReject(t *testing.T) {
	allow := New(Config{Limit: 10, Window: time.Second, MaxKeys: 1, OnFull: MaxKeysAllow})
	if d := allow.Take(1, true, 0, 1); !d.Allowed || d.Result != ResultAllowed {
		t.Fatalf("first key: %+v", d)
	}
	d := allow.Take(2, true, 0, 1)
	if !d.Allowed || d.Result != ResultFull {
		t.Fatalf("allow when full: %+v", d)
	}
	if allow.Take(1, true, 0, 1).Result == ResultFull {
		t.Fatal("a live key was treated as new")
	}

	reject := New(Config{Limit: 10, Window: time.Second, MaxKeys: 1, OnFull: MaxKeysReject})
	reject.Take(1, true, 0, 1)
	d = reject.Take(2, true, 0, 1)
	if d.Allowed || d.Result != ResultFull || d.RetryAfter != time.Second || !d.RetryKnown {
		t.Fatalf("reject when full: %+v", d)
	}
}

func TestIdleBucketExpires(t *testing.T) {
	const window = time.Second
	l := newTest(1, window, 10)
	if !l.Take(1, true, 0, 1).Allowed {
		t.Fatal("first")
	}
	if l.Take(1, true, 0, 1).Allowed {
		t.Fatal("second")
	}
	// The table drops a bucket once it has been unread for two windows plus a second.
	later := int64(2*window + time.Second + 1)
	d := l.Take(1, true, later, 1)
	if !d.Allowed || d.Result != ResultAllowed {
		t.Fatalf("expired bucket: %+v", d)
	}
}

func TestResultStrings(t *testing.T) {
	for _, tc := range []struct {
		r    Result
		want string
	}{
		{ResultAllowed, "allowed"},
		{ResultLimited, "limited"},
		{ResultCounted, "counted"},
		{ResultExempt, "exempt"},
		{ResultFull, "full"},
		{Result(99), ""},
	} {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("Result(%d) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	if got := RetryAfterSeconds(0); got != 1 {
		t.Fatalf("zero = %d", got)
	}
	if got := RetryAfterSeconds(time.Nanosecond); got != 1 {
		t.Fatalf("nanosecond = %d", got)
	}
	if got := RetryAfterSeconds(time.Second + 1); got != 2 {
		t.Fatalf("1s+1ns = %d", got)
	}
}

func TestChargeStoredKeyWhenTheTableIsFull(t *testing.T) {
	l := New(Config{Limit: 10, Window: time.Second, MaxKeys: 1, OnFull: MaxKeysReject})
	if d := l.Take(1, true, 0, 1); !d.Allowed || d.Result != ResultAllowed {
		t.Fatalf("first key: %+v", d)
	}
	if d := l.Take(2, true, 0, 1); d.Allowed || d.Result != ResultFull {
		t.Fatalf("second key: %+v", d)
	}
	l.Charge(2, true, 0, 5)
	if l.Len() != 1 {
		t.Fatalf("a refused key was stored, len %d", l.Len())
	}
	l.Charge(1, true, 0, 9)
	if l.Take(1, true, 0, 1).Allowed {
		t.Fatal("the charge against the stored key was dropped")
	}
}

func TestNonPositiveWindowDoesNotPanic(t *testing.T) {
	for _, window := range []time.Duration{0, -time.Second} {
		l := New(Config{Limit: 1, Window: window, MaxKeys: 10})
		for range 3 {
			d := l.Take(1, true, 0, 1)
			if !d.Allowed || d.Result != ResultAllowed {
				t.Fatalf("window %v take: %+v", window, d)
			}
			if d := l.Count(1, true, 0, 1); !d.Allowed {
				t.Fatalf("window %v count: %+v", window, d)
			}
		}
		l.Charge(1, true, 0, 5)
		if l.Len() != 0 {
			t.Fatalf("window %v stored %d buckets", window, l.Len())
		}
	}
	shape := Shape{Window: 0, Limit: 1, MaxKeys: 10}
	looked := Lookup("zero-window", shape, Policy{})
	if !looked.Take(1, true, 0, 1).Allowed || looked.Len() != 0 {
		t.Fatal("lookup of a zero window panicked or counted")
	}
	resetRegistry()
}

func TestNilLimiterLen(t *testing.T) {
	var l *Limiter
	if l.Len() != 0 {
		t.Fatal("nil limiter has a length")
	}
}
