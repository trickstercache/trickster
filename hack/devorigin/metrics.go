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
	"container/heap"
	"strconv"
	"strings"
)

type metricType string

const (
	counter   metricType = "counter"
	gauge     metricType = "gauge"
	histogram metricType = "histogram"
)

var distanceBounds = []int64{50, 100, 200, 300, 500, 1000, 2000} // hundredths of a mile; +Inf implied

var distanceLabels = []string{"0.5", "1", "2", "3", "5", "10", "20", "+Inf"}

type family struct {
	name    string
	typ     metricType
	help    string
	cents   bool // values are hundredths, rendered with two decimals
	metrics []*metric
	index   map[string]*metric
}

type metric struct {
	labels    string
	base      int // index in vals; histograms span their buckets (+Inf last), then a sum
	firstStep int
}

type boroughMetrics struct {
	tips, passengers, inProgress, distance *metric
}

type dropoff struct {
	at  int64
	idx int
}

type dropoffs []dropoff

func (d dropoffs) Len() int           { return len(d) }
func (d dropoffs) Less(i, j int) bool { return d[i].at < d[j].at }
func (d dropoffs) Swap(i, j int)      { d[i], d[j] = d[j], d[i] }
func (d *dropoffs) Push(x any)        { *d = append(*d, x.(dropoff)) }
func (d *dropoffs) Pop() any {
	old := *d
	x := old[len(old)-1]
	*d = old[:len(old)-1]
	return x
}

type accumulator struct {
	src        *tripSource
	vals       []int64
	steps      int
	trips      *family
	fares      *family
	tips       *family
	passengers *family
	inProgress *family
	distance   *family
	families   []*family
	byBorough  map[string]*boroughMetrics
	pending    dropoffs
	keyBuf     []byte
}

func newAccumulator(src *tripSource) *accumulator {
	a := &accumulator{
		src:        src,
		trips:      &family{name: "trips", typ: counter, help: "Trips started, by pickup borough, cab type and payment type."},
		fares:      &family{name: "trips_fares_dollars", typ: counter, help: "Total amount charged for trips started.", cents: true},
		tips:       &family{name: "trips_tips_dollars", typ: counter, help: "Tips paid on trips started.", cents: true},
		passengers: &family{name: "trips_passengers", typ: counter, help: "Passengers carried on trips started."},
		inProgress: &family{name: "trips_in_progress", typ: gauge, help: "Trips picked up and not yet dropped off."},
		distance:   &family{name: "trips_distance_miles", typ: histogram, help: "Distance of trips started.", cents: true},
		byBorough:  map[string]*boroughMetrics{},
	}
	a.families = []*family{a.trips, a.fares, a.tips, a.passengers, a.inProgress, a.distance}
	a.trips.index, a.fares.index = map[string]*metric{}, map[string]*metric{}
	return a
}

func (a *accumulator) advance(t int64) error {
	// values are a pure function of t: every trip since the start of the seed data
	// picked up by t is counted, and t must not decrease between calls
	for {
		tr, ok, err := a.src.peek()
		if err != nil {
			return err
		}
		if !ok || tr.pickup > t {
			break
		}
		a.src.consume()
		a.add(&tr)
	}
	for len(a.pending) > 0 && a.pending[0].at <= t {
		d := heap.Pop(&a.pending).(dropoff)
		a.vals[d.idx]--
	}
	return nil
}

func (a *accumulator) add(tr *trip) {
	b := a.borough(tr.borough)
	a.vals[a.labeled(a.trips, tr.borough, tr.cab, tr.payment).base]++
	a.vals[a.labeled(a.fares, tr.borough, tr.cab, "").base] += tr.total
	a.vals[b.tips.base] += tr.tip
	a.vals[b.passengers.base] += tr.passengers
	a.vals[b.inProgress.base]++
	heap.Push(&a.pending, dropoff{at: tr.dropoff, idx: b.inProgress.base})
	for i, bound := range distanceBounds {
		if tr.distance <= bound {
			a.vals[b.distance.base+i]++
		}
	}
	a.vals[b.distance.base+len(distanceBounds)]++
	a.vals[b.distance.base+len(distanceBounds)+1] += tr.distance
}

func (a *accumulator) borough(name string) *boroughMetrics {
	if b, ok := a.byBorough[name]; ok {
		return b
	}
	l := label("borough", name)
	b := &boroughMetrics{
		tips:       a.register(a.tips, l, 1),
		passengers: a.register(a.passengers, l, 1),
		inProgress: a.register(a.inProgress, l, 1),
		distance:   a.register(a.distance, l, len(distanceBounds)+2),
	}
	a.byBorough[name] = b
	return b
}

func (a *accumulator) labeled(f *family, borough, cab, payment string) *metric {
	a.keyBuf = append(a.keyBuf[:0], borough...)
	a.keyBuf = append(a.keyBuf, 0)
	a.keyBuf = append(a.keyBuf, cab...)
	a.keyBuf = append(a.keyBuf, 0)
	a.keyBuf = append(a.keyBuf, payment...)
	if m, ok := f.index[string(a.keyBuf)]; ok {
		return m
	}
	l := label("borough", borough) + "," + label("cab_type", cab)
	if payment != "" {
		l += "," + label("payment_type", payment)
	}
	m := a.register(f, l, 1)
	f.index[string(a.keyBuf)] = m
	return m
}

func (a *accumulator) register(f *family, labels string, width int) *metric {
	m := &metric{labels: labels, base: len(a.vals), firstStep: a.steps}
	a.vals = append(a.vals, make([]int64, width)...)
	f.metrics = append(f.metrics, m)
	return m
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func label(name, value string) string {
	return name + `="` + labelEscaper.Replace(value) + `"`
}

func appendSample(b []byte, name, labels string, v int64, cents bool, ts int64) []byte {
	b = append(b, name...)
	if labels != "" {
		b = append(b, '{')
		b = append(b, labels...)
		b = append(b, '}')
	}
	b = append(b, ' ')
	b = appendValue(b, v, cents)
	if ts >= 0 { // a negative ts omits the timestamp
		b = append(b, ' ')
		b = strconv.AppendInt(b, ts, 10)
	}
	return append(b, '\n')
}

func appendValue(b []byte, v int64, cents bool) []byte {
	if !cents {
		return strconv.AppendInt(b, v, 10)
	}
	// negating the quotient and remainder, not v, cannot overflow at MinInt64
	whole, r := v/100, v%100
	if v < 0 {
		b = append(b, '-')
		whole, r = -whole, -r
	}
	b = strconv.AppendInt(b, whole, 10)
	if r != 0 {
		b = append(b, '.', byte('0'+r/10))
		if r%10 != 0 {
			b = append(b, byte('0'+r%10))
		}
	}
	return b
}

func appendMetric(b []byte, f *family, m *metric, vals []int64, ts int64) []byte {
	switch f.typ {
	case counter:
		return appendSample(b, f.name+"_total", m.labels, vals[m.base], f.cents, ts)
	case histogram:
		nb := len(distanceLabels)
		for i, le := range distanceLabels {
			b = appendSample(b, f.name+"_bucket", m.labels+","+label("le", le), vals[m.base+i], false, ts)
		}
		b = appendSample(b, f.name+"_count", m.labels, vals[m.base+nb-1], false, ts)
		return appendSample(b, f.name+"_sum", m.labels, vals[m.base+nb], f.cents, ts)
	}
	return appendSample(b, f.name, m.labels, vals[m.base], f.cents, ts)
}

func (a *accumulator) appendText(b []byte) []byte {
	for _, f := range a.families {
		if len(f.metrics) == 0 {
			continue
		}
		name := f.name
		if f.typ == counter {
			name += "_total" // the 0.0.4 format names counters by their sample name
		}
		b = append(b, "# HELP "+name+" "+f.help+"\n# TYPE "+name+" "+string(f.typ)+"\n"...)
		for _, m := range f.metrics {
			b = appendMetric(b, f, m, a.vals, -1)
		}
	}
	return b
}
