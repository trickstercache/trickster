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

// Step returns the step for the Timeseries
func (me *MatrixEnvelope) Step() time.Duration {
	return me.StepDuration
}

// SetStep sets the step for the Timeseries
func (me *MatrixEnvelope) SetStep(step time.Duration) {
	me.StepDuration = step
}

// Merge merges the provided Timeseries list into the base Timeseries (in the order provided) and optionally sorts the merged Timeseries
func (me *MatrixEnvelope) Merge(sort bool, collection ...timeseries.Timeseries) {
	meMetrics := make(map[string]*model.SampleStream)
	for _, s := range me.Data.Result {
		meMetrics[s.Metric.String()] = s
	}
	for _, ts := range collection {
		if ts != nil {
			me2 := ts.(*MatrixEnvelope)
			for _, s := range me2.Data.Result {
				name := s.Metric.String()
				if _, ok := meMetrics[name]; !ok {
					meMetrics[name] = s
					me.Data.Result = append(me.Data.Result, s)
					continue
				}
				meMetrics[name].Values = append(meMetrics[name].Values, s.Values...)
			}
			me.ExtentList = append(me.ExtentList, me2.ExtentList...)
		}
	}
	me.ExtentList = timeseries.CompressExtents(me.ExtentList, me.StepDuration)
	if sort {
		me.Sort()
	}
}

// Copy returns a perfect copy of the base Timeseries
func (me *MatrixEnvelope) Copy() timeseries.Timeseries {
	resMe := &MatrixEnvelope{
		Status: me.Status,
		Data: MatrixData{
			ResultType: me.Data.ResultType,
			Result:     make([]*model.SampleStream, 0, len(me.Data.Result)),
		},
	}
	for _, ss := range me.Data.Result {
		newSS := &model.SampleStream{Metric: ss.Metric}
		newSS.Values = ss.Values
		resMe.Data.Result = append(resMe.Data.Result, newSS)
	}
	return resMe
}

// Crop returns a copy of the base Timeseries that has been cropped down to the provided Extents.
// Crop assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (me *MatrixEnvelope) Crop(e timeseries.Extent) timeseries.Timeseries {
	ts := me.Copy().(*MatrixEnvelope)
	for i, s := range ts.Data.Result {
		ss := &model.SampleStream{Metric: s.Metric, Values: []model.SamplePair{}}
		start := -1
		end := -1
		for i, val := range s.Values {
			t := val.Timestamp.Time()
			if t == e.End {
				// for cases where the first element is the only qualifying element,
				// start must be incremented or an empty response is returned
				if i == 0 {
					start = 0
				}
				end = i + 1
				break
			}
			if t.After(e.End) {
				end = i
				break
			}
			if t.Before(e.Start) {
				continue
			}
			if start == -1 && (t == e.Start || (e.End.After(t) && t.After(e.Start))) {
				start = i
			}
		}
		if start != -1 {
			if end == -1 {
				end = len(s.Values)
			}
			ss.Metric = s.Metric
			ss.Values = s.Values[start:end]
			ts.Data.Result[i] = ss
		} else {
			if i < len(ts.Data.Result) {
				ts.Data.Result = append(ts.Data.Result[:i], ts.Data.Result[i+1:]...)
			} else {
				ts.Data.Result = ts.Data.Result[:len(ts.Data.Result)-1]
			}
		}
	}
	return ts
}

// Sort sorts all Values in each Series chronologically by their timestamp
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

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (me *MatrixEnvelope) SetExtents(extents []timeseries.Extent) {
	me.ExtentList = extents
}

// Extents returns the Timeseries's ExentList
func (me *MatrixEnvelope) Extents() []timeseries.Extent {
	if len(me.ExtentList) == 0 {
		me.Extremes()
	}
	return me.ExtentList
}

// SeriesCount returns the number of individual Series in the Timeseries object
func (me *MatrixEnvelope) SeriesCount() int {
	return len(me.Data.Result)
}

// ValueCount returns the count of all values across all Series in the Timeseries object
func (me *MatrixEnvelope) ValueCount() int {
	c := 0
	for i := range me.Data.Result {
		c += len(me.Data.Result[i].Values)
	}
	return c
}

// Extremes returns the absolute start end times of a Timeseries, without respect to uncached gaps
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

// methods required for sorting Prometheus model.Times

// Len returns the length of an array of Prometheus model.Times
func (t Times) Len() int {
	return len(t)
}

// Less returns true if i comes before j
func (t Times) Less(i, j int) bool {
	return t[i].Before(t[j])
}

// Swap modifies an array by of Prometheus model.Times swapping the values in indexes i and j
func (t Times) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}
