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
	"github.com/Comcast/trickster/pkg/sort/times"
	"github.com/prometheus/common/model"
)

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
	me.isSorted = false
	if sort {
		me.Sort()
	}
}

// Copy returns a perfect copy of the base Timeseries
func (me *MatrixEnvelope) Copy() timeseries.Timeseries {
	// TODO - add new fields to copy
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

// CropToSize reduces the number of elements in the Timeseries to the provided count, by evicting elements
// using a least-recently-used methodology. Any timestamps newer than the provided time are removed before
// sizing, in order to support backfill tolerance
func (me *MatrixEnvelope) CropToSize(c int, t time.Time) {

	x := len(me.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		me.Data.Result = model.Matrix{}
		me.ExtentList = timeseries.ExtentList{}
		return
	}

	// Crop to the Backfill Tolerance Value if needed
	if me.ExtentList[x-1].End.After(t) {
		me.CropToRange(timeseries.Extent{Start: me.ExtentList[0].Start, End: t})
	}

	if len(me.Data.Result) == 0 || me.TimestampCount() <= c {
		return
	}

	for _, s := range me.Data.Result {
		l := len(s.Values)
		if l > 0 {

		}
	}
}

// CropToRange reduces the Timeseries down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (me *MatrixEnvelope) CropToRange(e timeseries.Extent) {
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

	// if the series extent is entirely inside the extent of the crop range, simply adjust down its ExtentList
	if me.ExtentList.Contains(e) {
		if me.ValueCount() == 0 {
			me.Data.Result = model.Matrix{}
		}
		me.ExtentList = me.ExtentList.Crop(e)
		return
	}

	if len(me.Data.Result) == 0 {
		me.ExtentList = me.ExtentList.Crop(e)
		return
	}

	deletes := make(map[int]bool)

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
			deletes[i] = true
		}
	}
	if len(deletes) > 0 {
		tmp := me.Data.Result[:0]
		for i, r := range me.Data.Result {
			if _, ok := deletes[i]; !ok {
				tmp = append(tmp, r)
			}
		}
		me.Data.Result = tmp
	}
	me.ExtentList = me.ExtentList.Crop(e)
}

// Sort sorts all Values in each Series chronologically by their timestamp
func (me *MatrixEnvelope) Sort() {

	if me.isSorted {
		return
	}

	tsm := map[time.Time]bool{}

	for i, s := range me.Data.Result { // []SampleStream
		m := make(map[time.Time]model.SamplePair)
		for _, v := range s.Values { // []SamplePair
			t := v.Timestamp.Time()
			tsm[t] = true
			m[t] = v
		}
		keys := make(times.Times, 0, len(m))
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

	tsl := make(times.Times, 0, len(tsm))
	for t := range tsm {
		tsl = append(tsl, t)
	}
	sort.Sort(tsl)
	me.timestamps = tsl
	me.isSorted = true

}

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (me *MatrixEnvelope) SetExtents(extents timeseries.ExtentList) {
	me.ExtentList = extents
}

// Extents returns the Timeseries's ExentList
func (me *MatrixEnvelope) Extents() timeseries.ExtentList {
	return me.ExtentList
}

// TimestampCount returns the number of unique timestamps across the timeseries
func (me *MatrixEnvelope) TimestampCount() int {
	me.Sort() // triggers the update of timestamps if needed
	return len(me.timestamps)
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
