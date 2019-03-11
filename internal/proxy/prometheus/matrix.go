/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package prometheus

import (
	"sort"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/prometheus/common/model"
)

// Times represents an array of Prometheus Times
type Times []model.Time

// Step ...
func (me *MatrixEnvelope) Step() time.Duration {
	return me.StepDuration
}

// SetStep ...
func (me *MatrixEnvelope) SetStep(step time.Duration) {
	me.StepDuration = step
}

// Merge ...
func (me *MatrixEnvelope) Merge(collection ...timeseries.Timeseries) {

	meMetrics := make(map[string]*model.SampleStream)

	for _, s := range me.Data.Result {
		meMetrics[s.Metric.String()] = s
	}

	for _, ts := range collection {
		if ts != nil {
			me2 := ts.(*MatrixEnvelope)
			for _, s := range me2.Data.Result {
				name := s.Metric.String()
				if o, ok := meMetrics[name]; !ok {
					meMetrics[name] = o
					continue
				}
				meMetrics[name].Values = append(meMetrics[name].Values, s.Values...)
			}
			me.ExtentList = append(me.ExtentList, me2.ExtentList...)
		}
	}
	me.Sort()
}

// Copy ...
func (me *MatrixEnvelope) Copy() timeseries.Timeseries {
	resMe := &MatrixEnvelope{
		Status: me.Status,
		Data: MatrixData{
			ResultType: me.Data.ResultType,
			Result:     make([]*model.SampleStream, 0, len(me.Data.Result)),
		},
	}
	for index := range me.Data.Result {
		resSampleSteam := *me.Data.Result[index]
		resMe.Data.Result[index] = &resSampleSteam
	}
	return resMe
}

// Crop ...
func (me *MatrixEnvelope) Crop(e timeseries.Extent) timeseries.Timeseries {

	ts := &MatrixEnvelope{
		Status: me.Status,
		Data: MatrixData{
			ResultType: rvMatrix,
			Result:     make([]*model.SampleStream, 0, len(me.Data.Result)),
		},
	}

	for _, s := range me.Data.Result {
		l := len(s.Values)
		ss := &model.SampleStream{Metric: s.Metric, Values: make([]model.SamplePair, 0, l)}
		start := 0
		end := 0

		for i, val := range s.Values {

			t := val.Timestamp.Time()
			if t.After(e.End) {
				break
			}

			if t.Before(e.Start) {
				continue
			}

			if start == 0 && (t == e.Start || (e.End.After(t) && t.After(e.Start))) {
				start = i
				continue
			}

			if end == 0 && (t == e.End) {
				end = i + 1
			}
		}

		if start > 0 {
			if end == 0 {
				end = len(s.Values) - 1
			}

			ss.Values = s.Values[start:end]
		}
		ts.Data.Result = append(ts.Data.Result, ss)
	}

	return ts
}

// Sort ...
func (me *MatrixEnvelope) Sort() {

	for i, s := range me.Data.Result { // []SampleStream

		m := make(map[model.Time]model.SamplePair)
		for _, v := range s.Values { // []SamplePair
			m[v.Timestamp] = v

		}

		keys := make(Times, 0, len(m))
		for key := range m {
			keys = append(keys, key)
		}

		sort.Sort(keys)
		sm := make([]model.SamplePair, 0, len(keys))
		for _, key := range keys {
			sm = append(sm, m[key])

		}
		me.Data.Result[i].Values = sm
	}
}

// SetExtents ...
func (me *MatrixEnvelope) SetExtents(extents []timeseries.Extent) {
	me.ExtentList = extents
}

// Extents ...
func (me *MatrixEnvelope) Extents() []timeseries.Extent {
	if len(me.ExtentList) == 0 {
		me.Extremes()
	}
	return me.ExtentList
}

// CalculateDeltas ...
func (me *MatrixEnvelope) CalculateDeltas(trq *timeseries.TimeRangeQuery) []timeseries.Extent {
	me.Extremes()
	misses := []time.Time{}
	for i := trq.Extent.Start; trq.Extent.End.After(i) || trq.Extent.End == i; i = i.Add(time.Second * time.Duration(trq.Step)) {
		found := false
		for j := range me.ExtentList {
			if i == me.ExtentList[j].Start || i == me.ExtentList[j].End || (i.After(me.ExtentList[j].Start) && me.ExtentList[j].End.After(i)) {
				found = true
				break
			}
		}
		if !found {
			misses = append(misses, i)
		}
	}
	// Find the fill and gap ranges
	ins := []timeseries.Extent{}
	e := time.Unix(0, 0)
	var inStart = e
	l := len(misses)
	for i := range misses {
		if inStart == e {
			inStart = misses[i]
		}
		if i+1 == l || misses[i+1] != misses[i].Add(me.StepDuration) {
			ins = append(ins, timeseries.Extent{Start: inStart, End: misses[i]})
			inStart = e
		}
	}
	return ins
}

// Extremes returns the times of the oldest and newest cached data points for the given query.
func (me *MatrixEnvelope) Extremes() []timeseries.Extent {
	r := me.Data.Result
	stamps := map[model.Time]bool{}
	// Get unique timestamps
	for s := range r {
		for v := range r[s].Values {
			stamps[r[s].Values[v].Timestamp] = true
		}
	}
	var keys Times
	// Sort the timestamps
	if len(stamps) > 0 {
		keys = make(Times, 0, len(stamps))
		for k := range stamps {
			keys = append(keys, k)
		}
		sort.Sort(keys)
		me.ExtentList = []timeseries.Extent{timeseries.Extent{Start: keys[0].Time(), End: keys[len(keys)-1].Time()}}
	}
	return me.ExtentList
}

func (t Times) Len() int {
	return len(t)
}

func (t Times) Less(i, j int) bool {
	return t[i].Before(t[j])
}

func (t Times) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}
