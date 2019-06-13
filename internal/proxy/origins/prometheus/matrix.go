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
	me.ExtentList = me.ExtentList.Compress(me.StepDuration)
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
		StepDuration: me.StepDuration,
		ExtentList:   make(timeseries.ExtentList, len(me.ExtentList)),
	}
	copy(resMe.ExtentList, me.ExtentList)
	for _, ss := range me.Data.Result {
		newSS := &model.SampleStream{Metric: ss.Metric}
		newSS.Values = ss.Values[:]
		resMe.Data.Result = append(resMe.Data.Result, newSS)
	}
	return resMe
}

// Crop returns a copy of the base Timeseries that has been cropped down to the provided Extents.
// Crop assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (me *MatrixEnvelope) Crop(e timeseries.Extent) {

	x := len(me.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		me.Data.Result = model.Matrix{}
		me.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the extent of the series is entirely outside the extent of the crop range, return empty set and bail
	if me.ExtentList.OutsideOf(e) {
		me.Data.Result = model.Matrix{}
		me.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the series extent is entirely inside the extent of the crop range, simple adjust down its ExtentList
	if me.ExtentList.Contains(e) {
		if me.ValueCount() == 0 {
			me.Data.Result = model.Matrix{}
		}
		me.ExtentList = me.ExtentList.Crop(e)
		return
	}

	for i, s := range me.Data.Result {
		start := -1
		end := -1
		for j, val := range s.Values {
			t := val.Timestamp.Time()
			if t.Equal(e.End) {
				// for cases where the first element is the only qualifying element,
				// start must be incremented or an empty response is returned
				if j == 0 || t.Equal(e.Start) || start == -1 {
					start = j
				}
				end = j + 1
				break
			}
			if t.After(e.End) {
				end = j
				break
			}
			if t.Before(e.Start) {
				continue
			}
			if start == -1 && (t.Equal(e.Start) || (e.End.After(t) && t.After(e.Start))) {
				start = j
			}
		}
		if start != -1 && len(s.Values) > 0 {
			if end == -1 {
				end = len(s.Values)
			}
			me.Data.Result[i].Values = s.Values[start:end]
		} else {
			if i < len(me.Data.Result) {
				me.Data.Result = append(me.Data.Result[:i], me.Data.Result[i+1:]...)
			} else {
				me.Data.Result = me.Data.Result[:len(me.Data.Result)-1]
			}
		}

	}
	me.ExtentList = me.ExtentList.Crop(e)
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
func (me *MatrixEnvelope) SetExtents(extents timeseries.ExtentList) {
	me.ExtentList = extents
}

// Extents returns the Timeseries's ExentList
func (me *MatrixEnvelope) Extents() timeseries.ExtentList {
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
