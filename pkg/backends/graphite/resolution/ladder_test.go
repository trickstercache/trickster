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

import (
	"errors"
	"testing"
	"time"
)

const (
	h = time.Hour
	d = 24 * time.Hour
)

func TestParseRetentions(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  bool
	}{
		{"10s:6h,60s:7d,10m:5y", "10s:6h,1m:1w,10m:5y", false},
		{"10s:6h,1min:7d,10min:5y", "10s:6h,1m:1w,10m:5y", false},
		{"60s:2d,5m:30d,1h:2y", "1m:2d,5m:30d,1h:2y", false},
		{"5m:90d", "5m:90d", false},
		{"30s:12h,5m:14d,1h:1y", "30s:12h,5m:2w,1h:1y", false},
		{"10:60", "10s:10m", false},   // bare seconds, point count
		{"10s:2160", "10s:6h", false}, // point count
		{"1m:1800d", "1m:1800d", false},
		{" 10s : 6h , 1m : 7d ", "10s:6h,1m:1w", false},
		{"", "", true},
		{"10s", "", true},
		{"10s:6h,5s:7d", "", true},  // step must increase
		{"10s:6h,15s:7d", "", true}, // step must be a multiple
		{"10s:6h,60s:3h", "", true}, // retention must increase
		{"10mon:1y", "", true},      // no such unit
		{"x:6h", "", true},
		{"10s:x", "", true},
		{"10s:99999999999999999999", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			l, err := ParseRetentions(tc.in)
			if tc.err {
				if err == nil || !errors.Is(err, ErrInvalidLadder) {
					t.Fatalf("expected ErrInvalidLadder, got %v (%v)", err, l)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if l.String() != tc.want {
				t.Errorf("got %s want %s", l, tc.want)
			}
			if l.State != StateComplete || l.Fingerprint() == "" || len(l.Fingerprint()) != 16 {
				t.Errorf("unexpected state/fingerprint: %v %q", l.State, l.Fingerprint())
			}
			l2, _ := ParseRetentions(tc.in)
			if l2.Fingerprint() != l.Fingerprint() || l.Clone().Fingerprint() != l.Fingerprint() {
				t.Error("fingerprint not stable")
			}
		})
	}
}

func TestLadderStepFor(t *testing.T) {
	l, _ := ParseRetentions("10s:6h,60s:7d,10m:5y")
	tests := []struct {
		age  time.Duration
		step time.Duration
	}{
		{0, 10 * time.Second}, {time.Second, 10 * time.Second}, {6 * h, 10 * time.Second},
		{6*h + time.Second, time.Minute}, {7 * d, time.Minute}, {7*d + time.Second, 10 * time.Minute},
		{5 * 365 * d, 10 * time.Minute}, {100 * 365 * d, 10 * time.Minute}, // saturates
	}
	for _, tc := range tests {
		s, ok := l.StepFor(tc.age)
		if !ok || s != tc.step {
			t.Errorf("age %v: got %v,%t want %v", tc.age, s, ok, tc.step)
		}
	}
	if l.MaxRetention() != 5*365*d || !l.Saturates(5*365*d+1) || l.Saturates(5*365*d) {
		t.Error("unexpected retention/saturation")
	}
	if StateUnknown.String()+StatePartial.String()+StateComplete.String() != "unknownpartialcomplete" {
		t.Error("state strings")
	}
}

func TestPartialLadder(t *testing.T) {
	p := NewPartial()
	if _, ok := p.StepFor(h); ok || p.MaxRetention() != 0 || p.Fingerprint() != "" || p.Saturates(h) {
		t.Error("empty partial ladder must answer nothing")
	}
	must := func(age, step time.Duration) {
		t.Helper()
		if err := p.Observe(age, step); err != nil {
			t.Fatal(err)
		}
	}
	must(2*h, time.Minute)
	must(8*h, time.Minute)
	must(20*h, 10*time.Minute)
	// bracketed by equal observations: known
	if s, ok := p.StepFor(5 * h); !ok || s != time.Minute {
		t.Errorf("expected 1m between equal observations, got %v %t", s, ok)
	}
	// younger than the youngest: the finest observed step
	if s, ok := p.StepFor(time.Minute); !ok || s != time.Minute {
		t.Errorf("expected 1m below youngest observation, got %v %t", s, ok)
	}
	// exact observation
	if s, ok := p.StepFor(20 * h); !ok || s != 10*time.Minute {
		t.Errorf("expected exact observation, got %v %t", s, ok)
	}
	// between observations with different steps: the boundary is unknown
	if _, ok := p.StepFor(12 * h); ok {
		t.Error("must not guess between differing observations")
	}
	// older than the oldest: unknown (could be a coarser rung)
	if _, ok := p.StepFor(30 * d); ok {
		t.Error("must not guess beyond the oldest observation")
	}
	// contradictions
	if err := p.Observe(5*h, 10*time.Minute); !errors.Is(err, ErrInconsistent) {
		t.Errorf("expected ErrInconsistent for a coarser step at a younger age, got %v", err)
	}
	if err := p.Observe(30*h, time.Minute); !errors.Is(err, ErrInconsistent) {
		t.Errorf("expected ErrInconsistent for a finer step at an older age, got %v", err)
	}
	if err := p.Observe(8*h, 10*time.Minute); !errors.Is(err, ErrInconsistent) {
		t.Errorf("expected ErrInconsistent for a conflicting repeat, got %v", err)
	}
	if err := p.Observe(8*h, time.Minute); err != nil {
		t.Errorf("a consistent repeat must be accepted: %v", err)
	}
	if p.String() != "partial[2h->1m,8h->1m,20h->10m]" {
		t.Errorf("unexpected string %s", p)
	}
	// a complete ladder accepts consistent observations and rejects others
	c, _ := ParseRetentions("10s:6h,60s:7d")
	if err := c.Observe(h, 10*time.Second); err != nil {
		t.Error(err)
	}
	if err := c.Observe(h, time.Minute); !errors.Is(err, ErrInconsistent) {
		t.Errorf("expected ErrInconsistent, got %v", err)
	}
	if _, err := NewLadder(nil); !errors.Is(err, ErrInvalidLadder) {
		t.Error("expected ErrInvalidLadder for no rungs")
	}
}

func TestLCM(t *testing.T) {
	if LCM(nil) != 0 || LCM([]time.Duration{0}) != 0 {
		t.Error("empty")
	}
	if LCM([]time.Duration{10 * time.Second, time.Minute}) != time.Minute {
		t.Error("10s,60s")
	}
	if LCM([]time.Duration{10 * time.Second, 15 * time.Second}) != 30*time.Second {
		t.Error("10s,15s")
	}
	if LCM([]time.Duration{time.Minute, 30 * time.Second, 0}) != time.Minute {
		t.Error("60s,30s")
	}
}

func TestFormatDur(t *testing.T) {
	for in, want := range map[time.Duration]string{
		10 * time.Second: "10s", 90 * time.Second: "90s", time.Minute: "1m", 6 * h: "6h",
		7 * d: "1w", 30 * d: "30d", 365 * d: "1y", 5 * 365 * d: "5y", 0: "0s",
	} {
		if got := formatDur(in); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}
