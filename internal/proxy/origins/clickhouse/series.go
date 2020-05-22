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
	"sync"
	"sync/atomic"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/pkg/sort/times"
)

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

	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}

	for _, ts := range collection {
		if ts != nil {
			re2 := ts.(*ResultsEnvelope)
			for k, s := range re2.Data {
				wg.Add(1)
				go func(l string, d *DataSet) {
					mtx.Lock()
					if _, ok := re.Data[l]; !ok {
						re.Data[l] = d
						mtx.Unlock()
						wg.Done()
						return
					}
					re.Data[l].Points = append(re.Data[l].Points, d.Points...)
					mtx.Unlock()
					wg.Done()
				}(k, s)
			}
			wg.Wait()
			re.mergeSeriesOrder(re2.SeriesOrder)
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

func (re *ResultsEnvelope) mergeSeriesOrder(so2 []string) {

	if len(so2) == 0 {
		return
	}

	if len(re.SeriesOrder) == 0 {
		re.SeriesOrder = so2
		return
	}

	so1 := make([]string, len(re.SeriesOrder), len(re.SeriesOrder)+len(so2))
	copy(so1, re.SeriesOrder)
	adds := make([]string, 0, len(so2))
	added := make(map[string]bool)

	for _, n := range so2 {
		if _, ok := re.Data[n]; !ok {
			if _, ok2 := added[n]; !ok2 {
				adds = append(adds, n)
				added[n] = true
			}
			continue
		}

		if len(adds) > 0 {
			for i, v := range so1 {
				if v == n {
					adds = append(adds, so1[i:]...)
					so1 = append(so1[0:i], adds...)
				}
			}
			adds = adds[:0]
		}
	}

	if len(adds) > 0 {
		so1 = append(so1, adds...)
	}

	re.SeriesOrder = so1

}

// Clone returns a perfect copy of the base Timeseries
func (re *ResultsEnvelope) Clone() timeseries.Timeseries {
	re2 := &ResultsEnvelope{
		isCounted:    re.isCounted,
		isSorted:     re.isSorted,
		StepDuration: re.StepDuration,
	}

	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}

	if re.SeriesOrder != nil {
		re2.SeriesOrder = make([]string, len(re.SeriesOrder))
		copy(re2.SeriesOrder, re.SeriesOrder)
	}

	if re.ExtentList != nil {
		re2.ExtentList = make(timeseries.ExtentList, len(re.ExtentList))
		copy(re2.ExtentList, re.ExtentList)
	}

	if re.tslist != nil {
		re2.tslist = make(times.Times, len(re.tslist))
		copy(re2.tslist, re.tslist)
	}

	if re.Meta != nil {
		re2.Meta = make([]FieldDefinition, len(re.Meta))
		copy(re2.Meta, re.Meta)
	}

	if re.Serializers != nil {
		re2.Serializers = make(map[string]func(interface{}))
		wg.Add(1)
		go func() {
			for k, s := range re.Serializers {
				re2.Serializers[k] = s
			}
			wg.Done()
		}()
	}

	if re.timestamps != nil {
		re2.timestamps = make(map[time.Time]bool)
		for k, v := range re.timestamps {
			wg.Add(1)
			go func(t time.Time, b bool) {
				mtx.Lock()
				re2.timestamps[t] = b
				mtx.Unlock()
				wg.Done()
			}(k, v)
		}
	}

	if re.Data != nil {
		re2.Data = make(map[string]*DataSet)
		wg.Add(1)
		go func() {
			for k, ds := range re.Data {
				ds2 := &DataSet{Metric: make(map[string]interface{})}
				for l, v := range ds.Metric {
					ds2.Metric[l] = v
				}
				ds2.Points = ds.Points[:]
				re2.Data[k] = ds2
			}
			wg.Done()
		}()
	}

	wg.Wait()

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
		re.Data = make(map[string]*DataSet)
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

	rc := tc - sz // # of required timestamps we must delete to meet the rentention policy
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

	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}

	for _, s := range re.Data {
		tmp := s.Points[:0]
		for _, r := range s.Points {
			wg.Add(1)
			go func(p Point) {
				mtx.Lock()
				if _, ok := removals[p.Timestamp]; !ok {
					tmp = append(tmp, p)
				}
				mtx.Unlock()
				wg.Done()
			}(r)
		}
		wg.Wait()
		s.Points = tmp
	}

	tl := times.FromMap(removals)
	sort.Sort(tl)

	for _, t := range tl {
		for i, e := range el {
			if e.StartsAt(t) {
				el[i].Start = e.Start.Add(re.StepDuration)
			}
		}
	}
	wg.Wait()

	re.ExtentList = timeseries.ExtentList(el).Compress(re.StepDuration)
	re.Sort()
}

// CropToRange reduces the Timeseries down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (re *ResultsEnvelope) CropToRange(e timeseries.Extent) {
	re.isCounted = false
	x := len(re.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		re.Data = make(map[string]*DataSet)
		re.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the extent of the series is entirely outside the extent of the crop range, return empty set and bail
	if re.ExtentList.OutsideOf(e) {
		re.Data = make(map[string]*DataSet)
		re.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the series extent is entirely inside the extent of the crop range, simply adjust down its ExtentList
	if re.ExtentList.InsideOf(e) {
		if re.ValueCount() == 0 {
			re.Data = make(map[string]*DataSet)
		}
		re.ExtentList = re.ExtentList.Crop(e)
		return
	}

	if len(re.Data) == 0 {
		re.ExtentList = re.ExtentList.Crop(e)
		return
	}

	deletes := make(map[string]bool)

	for i, s := range re.Data {
		start := -1
		end := -1
		for j, val := range s.Points {
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
		if start != -1 && len(s.Points) > 0 {
			if end == -1 {
				end = len(s.Points)
			}
			re.Data[i].Points = s.Points[start:end]
		} else {
			deletes[i] = true
		}
	}

	for i := range deletes {
		delete(re.Data, i)
	}

	re.ExtentList = re.ExtentList.Crop(e)
}

// Sort sorts all Values in each Series chronologically by their timestamp
func (re *ResultsEnvelope) Sort() {

	if re.isSorted || len(re.Data) == 0 {
		return
	}

	tsm := map[time.Time]bool{}
	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}

	for i, s := range re.Data {
		m := make(map[time.Time]Point)
		keys := make(times.Times, 0, len(s.Points))
		for _, v := range s.Points {
			wg.Add(1)
			go func(sp Point) {
				mtx.Lock()
				if _, ok := m[sp.Timestamp]; !ok {
					keys = append(keys, sp.Timestamp)
					m[sp.Timestamp] = sp
				}
				tsm[sp.Timestamp] = true
				mtx.Unlock()
				wg.Done()
			}(v)
		}
		wg.Wait()
		sort.Sort(keys)
		sm := make(Points, 0, len(keys))
		for _, key := range keys {
			sm = append(sm, m[key])
		}
		re.Data[i].Points = sm
	}

	sort.Sort(re.ExtentList)

	re.timestamps = tsm
	re.tslist = times.FromMap(tsm)
	re.isCounted = true
	re.isSorted = true
}

func (re *ResultsEnvelope) updateTimestamps() {

	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}

	if re.isCounted {
		return
	}
	m := make(map[time.Time]bool)
	for _, s := range re.Data {
		for _, v := range s.Points {
			wg.Add(1)
			go func(t time.Time) {
				mtx.Lock()
				m[t] = true
				mtx.Unlock()
				wg.Done()
			}(v.Timestamp)
		}
	}
	wg.Wait()
	re.timestamps = m
	re.tslist = times.FromMap(m)
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

// SeriesCount returns the number of individual Series in the Timeseries object
func (re *ResultsEnvelope) SeriesCount() int {
	return len(re.Data)
}

// ValueCount returns the count of all values across all Series in the Timeseries object
func (re *ResultsEnvelope) ValueCount() int {
	c := 0
	wg := sync.WaitGroup{}
	mtx := sync.Mutex{}
	for i := range re.Data {
		wg.Add(1)
		go func(j int) {
			mtx.Lock()
			c += j
			mtx.Unlock()
			wg.Done()
		}(len(re.Data[i].Points))
	}
	wg.Wait()
	return c
}

// Size returns the approximate memory utilization in bytes of the timeseries
func (re *ResultsEnvelope) Size() int {
	wg := sync.WaitGroup{}
	c := uint64(24 + // .stepDuration
		(25 * len(re.timestamps)) + // time.Time (24) + bool(1)
		(24 * len(re.tslist)) + // time.Time (24)
		(len(re.ExtentList) * 72) + // time.Time (24) * 3
		2, // .isSorted + .isCounted
	)
	for i := range re.Meta {
		wg.Add(1)
		go func(j int) {
			atomic.AddUint64(&c, uint64(len(re.Meta[j].Name)+len(re.Meta[j].Type)))
			wg.Done()
		}(i)
	}
	for _, s := range re.SeriesOrder {
		wg.Add(1)
		go func(t string) {
			atomic.AddUint64(&c, uint64(len(t)))
			wg.Done()
		}(s)
	}
	for k, v := range re.Data {
		atomic.AddUint64(&c, uint64(len(k)))
		wg.Add(1)
		go func(d *DataSet) {
			atomic.AddUint64(&c, uint64(len(d.Points)*32))
			for mk := range d.Metric {
				atomic.AddUint64(&c, uint64(len(mk)+8)) // + approx len of value (interface)
			}
			wg.Done()
		}(v)
	}
	wg.Wait()
	return int(c)
}
