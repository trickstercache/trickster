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

package clickhouse

import (
	"sort"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/pkg/sort/times"
)

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (re *ResultsEnvelope) SetExtents(extents timeseries.ExtentList) {
	el := make(timeseries.ExtentList, len(extents))
	copy(el, extents)
	re.ExtentList = el
	re.isCounted = false
}

// Extremes returns the absolute start and end times of a Timeseries, without respect to uncached gaps
func (re *ResultsEnvelope) Extremes() timeseries.ExtentList {

	// Bail if the results are empty
	if len(re.Data) == 0 {
		return nil
	}

	maxSize := 0
	for i := range re.Data {
		maxSize += len(re.Data[i].Points)
	}

	times := make([]time.Time, 0, maxSize)

	for i := range re.Data {
		for _, v := range re.Data[i].Points {
			// check the index of the time column again just in case it changed in the next series
			times = append(times, v.Timestamp)
		}
	}

	if len(times) == 0 {
		return nil
	}

	max := times[0]
	min := times[0]
	for i := range times {
		if times[i].After(max) {
			max = times[i]
		}
		if times[i].Before(min) {
			min = times[i]
		}
	}
	re.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: min, End: max}}
	return re.ExtentList
}

// Extents returns the Timeseries's ExentList
func (re *ResultsEnvelope) Extents() timeseries.ExtentList {
	if len(re.ExtentList) == 0 {
		return re.Extremes()
	}
	return re.ExtentList
}

// ValueCount returns the count of all values across all series in the Timeseries
func (re *ResultsEnvelope) ValueCount() int {
	c := 0
	for i := range re.Data {
		c += len(re.Data[i].Points)
	}
	return c
}

// SeriesCount returns the count of all Results in the Timeseries
// it is called SeriesCount due to Interface conformity and the disparity in nomenclature between various TSDBs.
func (re *ResultsEnvelope) SeriesCount() int {
	return len(re.Data)
}

// Step returns the step for the Timeseries
func (re *ResultsEnvelope) Step() time.Duration {
	return re.StepDuration
}

// SetStep sets the step for the Timeseries
func (re *ResultsEnvelope) SetStep(step time.Duration) {
	re.StepDuration = step
}

// Merge merges the provided Timeseries list into the base Timeseries (in the order provided) and optionally sorts the merged Timeseries
func (re *ResultsEnvelope) Merge(sort bool, collection ...timeseries.Timeseries) {

	if len(collection) == 0 {
		return
	}

	for _, ts := range collection {
		if ts == nil {
			continue
		}

		re2 := ts.(*ResultsEnvelope)
		for k, ds := range re2.Data {
			if _, ok := re.Data[k]; !ok {
				re.Data[k] = ds
				continue
			}
			re.Data[k].Points = append(re.Data[k].Points, ds.Points...)
		}
		re.ExtentList = append(re.ExtentList, re2.ExtentList...)

	}

	re.ExtentList = re.ExtentList.Compress(re.StepDuration)
	re.isSorted = false
	re.isCounted = false
	if sort {
		re.Sort()
	}

}

// Copy returns a perfect copy of the base Timeseries
func (re *ResultsEnvelope) Copy() timeseries.Timeseries {
	resultSe := &ResultsEnvelope{
		Meta: make([]FieldDefinition, len(re.Meta)),
		Data: make(map[string]*DataSet),
	}

	copy(resultSe.Meta, re.Meta)

	for k := range re.Data {
		resultSe.Data[k] = &DataSet{Metric: make(map[string]interface{}), Points: make([]Point, len(re.Data[k].Points))}
		for l, v := range re.Data[k].Metric {
			resultSe.Data[k].Metric[l] = v
		}
		copy(resultSe.Data[k].Points, re.Data[k].Points)
	}
	return resultSe
}

// CropToSize reduces the number of elements in the Timeseries to the provided count, by evicting elements
// using a least-recently-used methodology. The time parameter limits the upper extent to the provided time,
// in order to support backfill tolerance
func (re *ResultsEnvelope) CropToSize(sz int, t time.Time, lur timeseries.Extent) {
	re.isCounted = false
	re.isSorted = false

}

// CropToRange reduces the Timeseries down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (re *ResultsEnvelope) CropToRange(e timeseries.Extent) {

	re.isCounted = false

	if len(re.Data) == 0 || re.ValueCount() == 0 {
		return
	}

	ts := &ResultsEnvelope{
		Meta: make([]FieldDefinition, len(re.Meta)),
		Data: make(map[string]*DataSet),
	}

	copy(ts.Meta, re.Meta)

	for i, d := range re.Data {

		ds := &DataSet{
			Metric: make(map[string]interface{}),
		}

		for k, v := range d.Metric {
			ds.Metric[k] = v
		}

		start := -1
		end := -1
		for j, p := range d.Points {

			if p.Timestamp == e.End {
				if j == 0 {
					start = 0
				}
				end = j + 1
				break
			}

			if p.Timestamp.After(e.End) {
				end = j
				break
			}

			if p.Timestamp.Before(e.Start) {
				continue
			}

			if start == -1 && (p.Timestamp == e.Start || (e.End.After(p.Timestamp) && p.Timestamp.After(e.Start))) {
				start = j
			}

		}
		if start != -1 {
			if end == -1 {
				end = len(d.Points)
			}
			ds.Points = make([]Point, len(d.Points[start:end]))
			copy(ds.Points, d.Points[start:end])
		} else {
			ds.Points = []Point{}
		}

		ts.Data[i] = ds

	}
	return
}

// Sort sorts all Values in each Series chronologically by their timestamp
func (re *ResultsEnvelope) Sort() {

	if re.isSorted || len(re.Data) == 0 || re.ValueCount() == 0 {
		return
	}

	for k := range re.Data {
		sort.Sort(Points(re.Data[k].Points))
	}

	re.isSorted = true

}

// TimestampCount returns the count unique timestampes in across all series in the Timeseries
func (re *ResultsEnvelope) TimestampCount() int {
	if re.timestamps == nil {
		re.timestamps = make(map[time.Time]bool)
	}
	re.updateTimestamps()
	return len(re.timestamps)
}

func (re *ResultsEnvelope) updateTimestamps() {
	if re.isCounted {
		return
	}
	m := make(map[time.Time]bool)

	for _, d := range re.Data {
		for _, p := range d.Points {
			m[p.Timestamp] = true
		}
	}

	re.timestamps = m
	re.tslist = times.FromMap(m)
	re.isCounted = true
}
