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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testAnchor = 1_700_000_040 // a whole minute

func byName(ss []series) map[string][]series {
	m := map[string][]series{}
	for _, s := range ss {
		m[s.labels[0].value] = append(m[s.labels[0].value], s)
	}
	return m
}

func TestFixturesAreDeterministic(t *testing.T) {
	p1, g1 := buildFixtures(testAnchor)
	p2, g2 := buildFixtures(testAnchor)
	if !reflect.DeepEqual(p1, p2) || !reflect.DeepEqual(g1, g2) {
		t.Fatal("fixtures differ between builds")
	}
	// a later anchor reproduces the values at every shared timestamp
	p3, _ := buildFixtures(testAnchor + 3600)
	at := map[int64]float64{}
	for i, ts := range p1[0].ts {
		at[ts] = p1[0].vals[i]
	}
	for i, ts := range p3[0].ts {
		if v, ok := at[ts]; ok && v != p3[0].vals[i] {
			t.Fatalf("gauge at %d changed from %v to %v", ts, v, p3[0].vals[i])
		}
	}
	for _, s := range append(p1, g1...) {
		if len(s.ts) == 0 || len(s.ts) != len(s.vals) || !slices.IsSorted(s.ts) {
			t.Errorf("%v: %d timestamps, %d values", s.labels, len(s.ts), len(s.vals))
		}
		if s.ts[len(s.ts)-1] > testAnchor {
			t.Errorf("%v ends after the anchor", s.labels)
		}
	}
}

func TestPromFixtureShapes(t *testing.T) {
	prom, _ := buildFixtures(testAnchor)
	m := byName(prom)

	gauge := m["trickster_fixture_gauge"][0]
	if len(gauge.ts) != promWindow/15+1 {
		t.Errorf("gauge has %d samples", len(gauge.ts))
	}

	gaps := map[int64]bool{}
	irr := m["trickster_fixture_irregular"][0]
	for i := 1; i < len(irr.ts); i++ {
		gaps[irr.ts[i]-irr.ts[i-1]] = true
	}
	for _, d := range irregularIntervals {
		if !gaps[d] {
			t.Errorf("irregular fixture lacks a %ds interval", d)
		}
	}

	var full, partial bool
	res := m["trickster_fixture_resets_total"][0]
	for i := 1; i < len(res.vals); i++ {
		if res.vals[i] < res.vals[i-1] {
			full = full || res.vals[i] == 0
			partial = partial || res.vals[i] > 0
		}
	}
	if !full || !partial {
		t.Errorf("resets: full=%v partial=%v", full, partial)
	}

	miss := m["trickster_fixture_missing"][0]
	if n := len(miss.ts); n == 0 || n >= len(gauge.ts) {
		t.Errorf("missing fixture has %d of %d samples", n, len(gauge.ts))
	}

	var pos, neg bool
	for _, v := range m["trickster_fixture_special"][0].vals {
		pos, neg = pos || math.IsInf(v, 1), neg || math.IsInf(v, -1)
		if math.IsNaN(v) {
			t.Error("special fixture contains NaN")
		}
	}
	if !pos || !neg {
		t.Errorf("special fixture: +Inf=%v -Inf=%v", pos, neg)
	}

	a, b := m["trickster_fixture_name_a_total"], m["trickster_fixture_name_b_total"]
	if len(a) != 2 || len(b) != 2 || !reflect.DeepEqual(a[0].labels[1:], b[0].labels[1:]) {
		t.Errorf("name fixtures do not share label sets: %v / %v", a, b)
	}
}

func TestGraphiteFixtureShapes(t *testing.T) {
	_, graphite := buildFixtures(testAnchor)
	steps := map[string]int64{}
	branches := map[string]bool{}
	for _, s := range graphite {
		name := s.labels[0].value
		parts := strings.Split(name, ".")
		if parts[0] != graphiteRoot {
			t.Fatalf("%s is outside %s", name, graphiteRoot)
		}
		branches[parts[1]] = true
		steps[parts[1]] = s.ts[1] - s.ts[0]
	}
	if got := slices.Sorted(func(yield func(string) bool) {
		for b := range branches {
			if !yield(b) {
				return
			}
		}
	}); !slices.Equal(got, graphiteBranches()) {
		t.Errorf("branches %v", got)
	}
	if steps["fast"] != 10 || steps["tagged"] != 30 || steps["slow"] != 60 {
		t.Errorf("cadences %v", steps)
	}
	tagged := 0
	for _, s := range graphite {
		if s.labels[0].value == graphiteTagged {
			tagged++
			if len(s.labels) != 1+len(graphiteTagNames) {
				t.Errorf("tagged labels %v", s.labels)
			}
		}
	}
	if tagged != 4 {
		t.Errorf("%d tagged series", tagged)
	}
}

func TestAfterAndCanaries(t *testing.T) {
	prom, graphite := buildFixtures(testAnchor)
	if got := after(prom, testAnchor); len(got) != 0 {
		t.Errorf("after the anchor: %d series", len(got))
	}
	tail := after(prom, testAnchor-60)
	for _, s := range tail {
		if s.ts[0] <= testAnchor-60 || len(s.ts) != len(s.vals) {
			t.Errorf("%v kept %v", s.labels, s.ts)
		}
	}
	if len(tail) == 0 || len(after(prom, 0)) != len(prom) {
		t.Errorf("tail has %d series", len(tail))
	}
	// each canary has a sample at the anchor, so it marks the last seeding
	for _, c := range []struct {
		ss   []series
		name string
	}{{prom, "trickster_fixture_gauge"}, {graphite, graphiteCanary}} {
		s := byName(c.ss)[c.name]
		if len(s) != 1 || s[0].ts[len(s[0].ts)-1] != testAnchor {
			t.Errorf("canary %s does not end at the anchor", c.name)
		}
	}
	if !strings.Contains(fixtureCanary, "trickster_fixture_gauge") {
		t.Errorf("fixture canary %s", fixtureCanary)
	}
}

func TestWriteJSONLines(t *testing.T) {
	ss := []series{{
		labels: []label{{"__name__", `a"b`}, {"k", "v\\"}},
		ts:     []int64{1, 2, 3},
		vals:   []float64{1.25, math.Inf(1), math.Inf(-1)},
	}}
	var b bytes.Buffer
	if err := writeJSONLines(&b, ss); err != nil {
		t.Fatal(err)
	}
	want := `{"metric":{"__name__":"a\"b","k":"v\\"},"values":[1.25,"Inf","-Inf"],"timestamps":[1000,2000,3000]}` + "\n"
	if b.String() != want {
		t.Fatalf("got  %s\nwant %s", b.String(), want)
	}
	var v map[string]any
	if err := json.Unmarshal(b.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONLines(failWriter{}, ss); !errors.Is(err, errWriteFailed) {
		t.Fatalf("got %v", err)
	}
}
