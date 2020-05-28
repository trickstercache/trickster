/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package clickhouse

import (
	"sort"
	"time"

	"github.com/tricksterproxy/trickster/pkg/sort/times"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// Step returns the step for the Timeseries
func (re *ResultsEnvelope) Step() time.Duration {
	return re.StepDuration
}

// SetStep sets the step for the Timeseries
func (re *ResultsEnvelope) SetStep(step time.Duration) {
	re.StepDuration = step
}

// Merges the provided Timeseries list into the base Timeseries (in the order provided)
// and optionally sorts the merged Timeseries
func (re *ResultsEnvelope) Merge(sort bool, collection ...timeseries.Timeseries) {
	for _, ts := range collection {
		if ts != nil {
			re2 := ts.(*ResultsEnvelope)
			re.Data = append(re.Data, re2.Data...)
			re.ExtentList = append(re.ExtentList, re2.ExtentList...)
		}
	}

	re.ExtentList = re.ExtentList.Compress(re.StepDuration)
	re.isSorted = false
	re.isCounted = false
	if sort {
		re.Sort()
	}
}

// Returns a perfect copy of the base Timeseries
func (re *ResultsEnvelope) Clone() timeseries.Timeseries {
	re2 := &ResultsEnvelope{
		isCounted:    re.isCounted,
		isSorted:     re.isSorted,
		StepDuration: re.StepDuration,
	}

	if re.ExtentList != nil {
		re2.ExtentList = make(timeseries.ExtentList, len(re.ExtentList))
		copy(re2.ExtentList, re.ExtentList)
	}

	if re.tsList != nil {
		re2.tsList = make(times.Times, len(re.tsList))
		copy(re2.tsList, re.tsList)
	}

	if re.Meta != nil {
		re2.Meta = make([]FieldDefinition, len(re.Meta))
		copy(re2.Meta, re.Meta)
	}

	if re.timestamps != nil {
		re2.timestamps = make(map[time.Time]bool)
		for k, v := range re.timestamps {
			re2.timestamps[k] = v
		}
	}

	if re.Data != nil {
		re2.Data = make([]Point, 0)
		for _, p1 := range re.Data {
			p2 := Point{Timestamp: p1.Timestamp}
			for _, rv := range p1.Values {
				pm := ResponseValue{}
				for k, v := range rv {
					pm[k] = v
				}
				p2.Values = append(p2.Values, pm)
			}
			re2.Data = append(re2.Data, p2)
		}
	}
	return re2
}

// CropToSize reduces the number of elements in the Timeseries to the provided count, by evicting elements
// using a least-recently-used methodology. Any timestamps newer than the provided time are removed before
// sizing, in order to support backfill tolerance. The provided extent will be marked as used during crop.
func (re *ResultsEnvelope) CropToSize(sz int, t time.Time, lur timeseries.Extent) {
	re.isCounted = false
	re.isSorted = false
	x := len(re.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		re.Data = make([]Point, 0)
		re.ExtentList = timeseries.ExtentList{}
		return
	}

	// Crop to the Backfill Tolerance Value if needed
	if re.ExtentList[x-1].End.After(t) {
		re.CropToRange(timeseries.Extent{Start: re.ExtentList[0].Start, End: t})
	}

	tc := re.TimestampCount()
	el := timeseries.ExtentListLRU(re.ExtentList).UpdateLastUsed(lur, re.StepDuration)
	sort.Sort(el)
	if len(re.Data) == 0 || tc <= sz {
		return
	}

	rc := tc - sz // # of required timestamps we must delete to meet the retention policy
	removals := make(map[time.Time]bool)
	done := false
	var ok bool

	for _, x := range el {
		for ts := x.Start; !x.End.Before(ts) && !done; ts = ts.Add(re.StepDuration) {
			if _, ok = re.timestamps[ts]; ok {
				removals[ts] = true
				done = len(removals) >= rc
			}
		}
		if done {
			break
		}
	}

	tmp := make([]Point, 0, len(re.Data)-len(removals))
	for _, p := range re.Data {
		if _, ok := removals[p.Timestamp]; !ok {
			tmp = append(tmp, p)
		}
	}
	re.Data = tmp

	tl := times.FromMap(removals)
	sort.Sort(tl)

	for _, t := range tl {
		for i, e := range el {
			if e.StartsAt(t) {
				el[i].Start = e.Start.Add(re.StepDuration)
			}
		}
	}

	re.ExtentList = timeseries.ExtentList(el).Compress(re.StepDuration)
	re.Sort()
}

// CropToRange reduces the Timeseries down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (re *ResultsEnvelope) CropToRange(e timeseries.Extent) {
	re.isCounted = false

	// The Series has no extents, or is outside of the crop range, so no need to do anything
	if len(re.ExtentList) < 1 || re.ExtentList.OutsideOf(e) {
		re.Data = make([]Point, 0)
		re.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the series extent is entirely inside the extent of the crop range, simply adjust down its ExtentList
	if re.ExtentList.InsideOf(e) {
		if re.ValueCount() == 0 {
			re.Data = make([]Point, 0)
		}
		re.ExtentList = re.ExtentList.Crop(e)
		return
	}

	if len(re.Data) == 0 {
		re.ExtentList = re.ExtentList.Crop(e)
		return
	}

	start := -1
	end := -1
	for j, val := range re.Data {
		t := val.Timestamp
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
	if start != -1 && len(re.Data) > 0 {
		if end == -1 {
			end = len(re.Data)
		}
		re.Data = re.Data[start:end]
	}

	re.ExtentList = re.ExtentList.Crop(e)
}

// Sorts all Points chronologically by their timestamp
func (re *ResultsEnvelope) Sort() {

	if re.isSorted || len(re.Data) == 0 {
		return
	}

	tsm := map[time.Time]bool{}
	m := make(map[time.Time]Point)
	keys := make(times.Times, 0, len(re.Data))
	for _, v := range re.Data {
		if _, ok := m[v.Timestamp]; !ok {
			keys = append(keys, v.Timestamp)
			m[v.Timestamp] = v
		}
		tsm[v.Timestamp] = true
	}
	sort.Sort(keys)
	sm := make([]Point, 0, len(keys))
	for _, key := range keys {
		sm = append(sm, m[key])
	}
	re.Data = sm
	sort.Sort(re.ExtentList)

	re.timestamps = tsm
	re.tsList = times.FromMap(tsm)
	re.isCounted = true
	re.isSorted = true
}

func (re *ResultsEnvelope) updateTimestamps() {
	if re.isCounted {
		return
	}
	m := make(map[time.Time]bool)
	for _, p := range re.Data {
		m[p.Timestamp] = true
	}
	re.timestamps = m
	re.tsList = times.FromMap(m)
	re.isCounted = true
}

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (re *ResultsEnvelope) SetExtents(extents timeseries.ExtentList) {
	re.isCounted = false
	re.ExtentList = extents
}

// Extents returns the Timeseries's ExentList
func (re *ResultsEnvelope) Extents() timeseries.ExtentList {
	return re.ExtentList
}

// TimestampCount returns the number of unique timestamps across the timeseries
func (re *ResultsEnvelope) TimestampCount() int {
	re.updateTimestamps()
	return len(re.timestamps)
}

// ValueCount returns the count of all values across all Series in the Timeseries object
func (re *ResultsEnvelope) ValueCount() int {
	return len(re.Data)
}

// Size returns the approximate memory utilization in bytes of the timeseries
func (re *ResultsEnvelope) Size() int {
	var size int
	for _, m := range re.Meta {
		size += len(m.Name) + len(m.Type)
	}

	for _, p := range re.Data {
		size += 8 // Timestamp guess
		for _, rv := range p.Values {
			for k2 := range rv {
				size += len(k2) + 16 // Key length + values guess
			}
		}
	}

	// ExtentList + StepDuration + Timestamps + Times + isCounted + isSorted
	size += (len(re.ExtentList) * 24) + 8 + (len(re.timestamps) * 9) + (len(re.tsList) * 8) + 2
	return size
}
