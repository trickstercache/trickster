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
	"encoding/json"
	"io"
	"math"
	"slices"
	"strconv"
)

// Every fixture value is a function of its absolute timestamp, so reseeding
// reproduces the same answers for any range both seedings cover.

const (
	fixturesJob      = "trickster_fixtures"
	fixturesInstance = "fixtures"
	graphiteRoot     = "vmgraphite"
	graphiteTagged   = graphiteRoot + ".tagged.latency"

	// canaries have a sample at every anchor, so their newest sample marks the
	// last seeding; all fixture series are generated through that same anchor
	fixtureCanary  = `trickster_fixture_gauge{job="trickster_fixtures"}`
	graphiteCanary = graphiteRoot + ".fast.us-east.web01.requests"

	// fixtures end at the seeding time, so these windows keep relative
	// dashboard ranges populated for days between reseeds
	promWindow     = 48 * 3600
	shortWindow    = 48 * 3600 // 10s and 30s graphite series
	longWindow     = 7 * 24 * 3600
	resetPeriod    = 3600
	partialResetAt = 1800
	// subtracted from minute counts so name-collision counters stay small
	nameCounterBase = 28_000_000
)

// irregularIntervals is the repeating gap pattern, in seconds, between
// trickster_fixture_irregular samples; the pattern restarts every 451s.
var irregularIntervals = []int64{7, 15, 23, 61, 300, 15, 30}

var graphiteTagNames = []string{"dc", "tier"}

type label struct{ name, value string }

type series struct {
	labels []label // __name__ first
	ts     []int64 // unix seconds
	vals   []float64
}

func graphiteBranches() []string {
	return []string{"fast", "gappy", "slow", "tagged"}
}

// noise returns a deterministic value in [0,1) using the splitmix64 finalizer.
func noise(seed, t int64) float64 {
	x := uint64(seed)<<40 ^ uint64(t) //nolint:gosec // bit mixing, not a conversion of meaning
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	x ^= x >> 31
	return float64(x>>11) / (1 << 53)
}

func wave(t, period int64, phase float64) float64 {
	return math.Sin(2*math.Pi*float64(t)/float64(period) + phase)
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

// grid samples value at each multiple of step in [from, to] that keep accepts.
func grid(labels []label, from, to, step int64, keep func(int64) bool, value func(int64) float64) series {
	s := series{labels: labels}
	for t := from + (step-from%step)%step; t <= to; t += step {
		if keep == nil || keep(t) {
			s.ts = append(s.ts, t)
			s.vals = append(s.vals, value(t))
		}
	}
	return s
}

func promLabels(name string, extra ...label) []label {
	return append([]label{{"__name__", name}, {"job", fixturesJob}, {"instance", fixturesInstance}}, extra...)
}

// buildFixtures returns the MetricsQL edge-case series and the Graphite series
// ending at anchor. NaN is absent because VictoriaMetrics drops it on import.
func buildFixtures(anchor int64) (prom, graphite []series) {
	from := anchor - promWindow
	prom = []series{
		grid(promLabels("trickster_fixture_gauge"), from, anchor, 15, nil, func(t int64) float64 {
			return round(100+25*wave(t, 3600, 0)+5*noise(1, t), 3)
		}),
		irregular(from, anchor),
		resets(from, anchor),
		grid(promLabels("trickster_fixture_missing"), from, anchor, 15, func(t int64) bool {
			m := (t / 60) % 10
			return m != 3 && m != 4 && t%21600 >= 1200
		}, func(t int64) float64 {
			return float64(10 + (t/15)%20)
		}),
		grid(promLabels("trickster_fixture_special"), from, anchor, 60, nil, func(t int64) float64 {
			return [...]float64{1.5, math.Inf(1), 2.5, math.Inf(-1)}[(t/60)%4]
		}),
	}
	// identical label sets under two names, for metric-name-sensitive expressions
	for _, shard := range []int64{1, 2} {
		extra := label{"shard", strconv.FormatInt(shard, 10)}
		prom = append(prom,
			grid(promLabels("trickster_fixture_name_a_total", extra), from, anchor, 60, nil, func(t int64) float64 {
				return float64(t/60 - nameCounterBase)
			}),
			grid(promLabels("trickster_fixture_name_b_total", extra), from, anchor, 60, nil, func(t int64) float64 {
				return float64(2*(t/60-nameCounterBase) + shard)
			}),
		)
	}
	return prom, graphiteFixtures(anchor)
}

func irregular(from, to int64) series {
	s := series{labels: promLabels("trickster_fixture_irregular")}
	var cycle int64
	for _, d := range irregularIntervals {
		cycle += d
	}
	t := from - from%cycle
	for i := 0; t <= to; i++ {
		if t >= from {
			s.ts = append(s.ts, t)
			s.vals = append(s.vals, round(50+10*wave(t, 1800, 0), 3))
		}
		t += irregularIntervals[i%len(irregularIntervals)]
	}
	return s
}

// resets is a 15s counter that restarts at zero every hour and drops to a
// quarter of its value at each half hour, so both reset shapes appear.
func resets(from, to int64) series {
	s := series{labels: promLabels("trickster_fixture_resets_total")}
	var counter float64
	for t := from - from%resetPeriod; t <= to; t += 15 {
		switch t % resetPeriod {
		case 0:
			counter = 0
		case partialResetAt:
			counter = math.Floor(counter / 4)
		default:
			counter += float64(1 + (t/15)%5)
		}
		if t >= from {
			s.ts = append(s.ts, t)
			s.vals = append(s.vals, counter)
		}
	}
	return s
}

// graphiteFixtures are dotted Graphite names at 10s, 30s and 60s cadences,
// with periodic gaps and a tagged family whose tags are the dc and tier labels.
func graphiteFixtures(anchor int64) []series {
	var out []series
	var i int64
	for _, region := range []string{"us-east", "us-west"} {
		for _, host := range []string{"web01", "web02"} {
			seed, phase := 10+i, float64(i)*math.Pi/4
			name := graphiteRoot + ".fast." + region + "." + host + ".requests"
			out = append(out, grid([]label{{"__name__", name}}, anchor-shortWindow, anchor, 10, nil, func(t int64) float64 {
				return math.Round(100 + 40*wave(t, 3600, phase) + 10*noise(seed, t))
			}))
			i++
		}
		seed, phase := 20+i, float64(i)*math.Pi/3
		name := graphiteRoot + ".slow." + region + ".db01.connections"
		out = append(out, grid([]label{{"__name__", name}}, anchor-longWindow, anchor, 60, nil, func(t int64) float64 {
			return math.Round(20 + 8*wave(t, 86400, phase) + 2*noise(seed, t))
		}))
	}
	out = append(out, grid([]label{{"__name__", graphiteRoot + ".gappy.web01.errors"}}, anchor-longWindow, anchor, 60,
		func(t int64) bool {
			m := (t / 60) % 15
			return (m < 5 || m > 7) && t%43200 >= 3600
		}, func(t int64) float64 {
			return math.Floor(5 * noise(30, t))
		}))
	base := map[string]float64{"us-east": 20, "us-west": 35, "web": 5, "db": 12}
	for _, dc := range []string{"us-east", "us-west"} {
		for _, tier := range []string{"web", "db"} {
			level, phase := base[dc]+base[tier], base[tier]/10
			labels := []label{{"__name__", graphiteTagged}, {"dc", dc}, {"tier", tier}}
			out = append(out, grid(labels, anchor-shortWindow, anchor, 30, nil, func(t int64) float64 {
				return round(level+3*wave(t, 900, phase), 2)
			}))
		}
	}
	return out
}

// after returns the samples newer than ts, omitting series left empty.
func after(ss []series, ts int64) []series {
	var out []series
	for _, s := range ss {
		i, _ := slices.BinarySearch(s.ts, ts+1)
		if i < len(s.ts) {
			out = append(out, series{labels: s.labels, ts: s.ts[i:], vals: s.vals[i:]})
		}
	}
	return out
}

// writeJSONLines encodes series in VictoriaMetrics' JSON line import format.
func writeJSONLines(w io.Writer, ss []series) error {
	var b []byte
	for _, s := range ss {
		b = append(b[:0], `{"metric":{`...)
		for i, l := range s.labels {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, l.name)
			b = append(b, ':')
			b = appendJSONString(b, l.value)
		}
		b = append(b, `},"values":[`...)
		for i, v := range s.vals {
			if i > 0 {
				b = append(b, ',')
			}
			switch {
			case math.IsInf(v, 1):
				b = append(b, `"Inf"`...)
			case math.IsInf(v, -1):
				b = append(b, `"-Inf"`...)
			default:
				b = strconv.AppendFloat(b, v, 'g', -1, 64)
			}
		}
		b = append(b, `],"timestamps":[`...)
		for i, ts := range s.ts {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, ts*1000, 10)
		}
		b = append(b, "]}\n"...)
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func appendJSONString(b []byte, s string) []byte {
	q, _ := json.Marshal(s)
	return append(b, q...)
}
