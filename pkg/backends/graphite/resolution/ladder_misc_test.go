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
	"testing"
	"time"
)

func TestAlignInterval(t *testing.T) {
	step := 10 * time.Second
	tests := []struct {
		from, until int64
		start, end  int64
	}{
		{1005, 1025, 1010, 1030}, // (t - t%step) + step on both edges
		{1000, 1020, 1010, 1030}, // aligned inputs still shift by one step
		{1005, 1006, 1010, 1020}, // zero-length result widened by one step
		{-15, -5, -10, 0},        // negative timestamps floor toward -inf
		{-10, 0, 0, 10},          // the epoch itself is a bucket boundary
		{0, 10, 10, 20},          // and from the other side of it
	}
	for _, tc := range tests {
		s, e := AlignInterval(time.Unix(tc.from, 0), time.Unix(tc.until, 0), step)
		if s.Unix() != tc.start || e.Unix() != tc.end {
			t.Errorf("[%d,%d]: got [%d,%d) want [%d,%d)", tc.from, tc.until, s.Unix(), e.Unix(), tc.start, tc.end)
		}
	}
	if s, e := AlignInterval(time.Unix(1005, 0), time.Unix(1025, 0), 0); s.Unix() != 1005 || e.Unix() != 1025 {
		t.Error("a zero step must not align")
	}
	// the request window is the inverse: asking for [start, end] returns
	// exactly those buckets
	start, end := time.Unix(1010, 0), time.Unix(1020, 0)
	from, until := RequestWindow(start, end, step)
	if s, e := AlignInterval(from, until, step); !s.Equal(start) || e.Unix() != 1030 {
		t.Errorf("round trip: got [%v,%v)", s.Unix(), e.Unix())
	}
	if BucketPhase != 0 {
		t.Error("graphite buckets are epoch-aligned")
	}
}

func TestClamp(t *testing.T) {
	now := time.Unix(100000, 0)
	ret := 1000 * time.Second
	tests := []struct {
		name         string
		from, until  int64
		wantF, wantU int64
		ok           bool
	}{
		{"inside", 99500, 99900, 99500, 99900, true},
		{"from in the future", 100001, 100002, 100001, 100002, false},
		{"until before oldest", 98000, 98999, 98000, 98999, false},
		{"until at oldest", 98000, 99000, 99000, 99000, true},
		{"from clamped", 98000, 99500, 99000, 99500, true},
		{"until clamped", 99500, 100500, 99500, 100000, true},
		{"both clamped", 0, 200000, 99000, 100000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, u, ok := Clamp(time.Unix(tc.from, 0), time.Unix(tc.until, 0), now, ret)
			if ok != tc.ok || f.Unix() != tc.wantF || u.Unix() != tc.wantU {
				t.Errorf("got [%d,%d] %t want [%d,%d] %t", f.Unix(), u.Unix(), ok, tc.wantF, tc.wantU, tc.ok)
			}
		})
	}
	// unknown retention: only the until clamp
	f, u, ok := Clamp(time.Unix(0, 0), time.Unix(200000, 0), now, 0)
	if !ok || f.Unix() != 0 || u.Unix() != 100000 {
		t.Errorf("unknown retention: got [%d,%d] %t", f.Unix(), u.Unix(), ok)
	}
}

func TestStatic(t *testing.T) {
	s, err := NewStatic([][2]string{
		{`^dev\.fast\.`, "10s:6h,60s:7d,10m:5y"},
		{`^dev\.`, "60s:2d"},
		{`.*`, "5m:90d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 3 {
		t.Error("len")
	}
	for leaf, want := range map[string]string{
		"dev.fast.cpu.host01.percent": "10s:6h,1m:1w,10m:5y",
		"dev.medium.orders.count":     "1m:2d",
		"carbon.agents.x.cpuUsage":    "5m:90d",
		"x.dev.fast.y":                "5m:90d", // ^ anchors; first match wins in order
	} {
		l, ok := s.Match(leaf)
		if !ok || l.String() != want {
			t.Errorf("%s: got %v %t want %s", leaf, l, ok, want)
		}
	}
	// re.search semantics: an unanchored pattern matches anywhere
	s2, _ := NewStatic([][2]string{{`fast`, "10s:6h"}})
	if _, ok := s2.Match("x.fast.y"); !ok {
		t.Error("unanchored pattern must match inside the path")
	}
	if _, ok := s2.Match("x.slow.y"); ok {
		t.Error("no match expected")
	}
	var nilStatic *Static
	if _, ok := nilStatic.Match("a"); ok || nilStatic.Len() != 0 {
		t.Error("nil static must match nothing")
	}
	if _, err := NewStatic([][2]string{{`(`, "10s:6h"}}); err == nil {
		t.Error("expected a regexp error")
	}
	if _, err := NewStatic([][2]string{{`a`, "bogus"}}); err == nil {
		t.Error("expected a retention error")
	}
}

func TestConfidenceStrings(t *testing.T) {
	if Unknown.String()+Exact.String()+Derived.String()+Configured.String() != "unknownexactderivedconfigured" {
		t.Error("frozen confidence label values changed")
	}
	if Confidence(99).String() != "unknown" {
		t.Error("out-of-range confidence")
	}
	var o NopObserver
	o.Lookup("", "")
	o.Probe("", "")
	o.Ladders(0)
	o.RegistryEntries("", 0)
}
