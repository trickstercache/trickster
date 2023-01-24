/*
 * Copyright 2018 The Trickster Authors
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

//go:generate msgp

// Package dataset defines the interface for managing time seres objects
// and provides time range manipulation capabilities
package dataset

import (
	"io"
	"sort"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// DataSet is the Common Time Series Format that Trickster uses to
// accelerate most of its supported TSDB backends
// DataSet conforms to the Timeseries interface
type DataSet struct {
	// Status is the optional status indicator for the DataSet
	Status string `msg:"status"`
	// ExtentList is the list of Extents (time ranges) represented in the Results
	ExtentList timeseries.ExtentList `msg:"extent_list"`
	// Results is the list of type Result. Each Result represents information about a
	// different statement in the source query for this DataSet
	Results []*Result `msg:"results"`
	// UpdateLock is used to synchronize updates to the DataSet
	UpdateLock sync.Mutex `msg:"-"`
	// Error is a container for any DataSet-level Errors
	Error string `msg:"error"`
	// ErrorType describes the type for any DataSet-level Errors
	ErrorType string `msg:"errorType"`
	// Warnings is a container for any DataSet-level Warnings
	Warnings []string `msg:"warnings"`
	// TimeRangeQuery is the trq associated with the Timeseries
	TimeRangeQuery *timeseries.TimeRangeQuery `msg:"trq"`
	// VolatileExtents is the list extents in the dataset that should be refreshed
	// on the next request to the Origin
	VolatileExtentList timeseries.ExtentList `msg:"volatile_extents"`
	// Sorter is the DataSet's Sort function, which defaults to DefaultSort
	Sorter func() `msg:"-"`
	// Merger is the DataSet's Merge function, which defaults to DefaultMerge
	Merger func(sortSeries bool, ts ...timeseries.Timeseries) `msg:"-"`
	// SizeCropper is the DataSet's CropToSize function, which defaults to DefaultSizeCropper
	SizeCropper func(int, time.Time, timeseries.Extent) `msg:"-"`
	// RangeCropper is the DataSet's CropToRange function, which defaults to DefaultRangeCropper
	RangeCropper func(timeseries.Extent) `msg:"-"`
}

// Marshaler is a function that serializes the provided DataSet into a byte slice
type Marshaler func(*DataSet, *timeseries.RequestOptions, int, io.Writer) error

// CroppedClone returns a new, perfect copy of the DataSet, efficiently
// cropped to the provided Extent. CroppedClone assumes the DataSet is sorted.
func (ds *DataSet) CroppedClone(e timeseries.Extent) timeseries.Timeseries {
	x := len(ds.ExtentList)
	if x == 0 || ds.Results == nil {
		return ds.Clone()
	}
	// if the series extent is entirely inside the extent of the crop range,
	// simply adjust down its ExtentList
	if ds.ExtentList.EncompassedBy(e) {
		clone := ds.Clone().(*DataSet)
		clone.ExtentList = clone.ExtentList.Crop(e)
		return clone
	}

	clone := &DataSet{
		Error:        ds.Error,
		Sorter:       ds.Sorter,
		Merger:       ds.Merger,
		SizeCropper:  ds.SizeCropper,
		RangeCropper: ds.RangeCropper,
		Results:      make([]*Result, len(ds.Results)),
	}
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	if ds.TimeRangeQuery != nil {
		clone.TimeRangeQuery = ds.TimeRangeQuery.Clone()
	}
	// if the extent of the series is entirely outside the extent of the crop
	// range, return empty set and bail
	if ds.ExtentList.OutsideOf(e) {
		for i := range ds.Results {
			if ds.Results[i] == nil {
				continue
			}
			clone.Results[i] = &Result{
				StatementID: ds.Results[i].StatementID,
				Error:       ds.Results[i].Error,
				SeriesList:  make([]*Series, 0),
			}
		}
		clone.ExtentList = timeseries.ExtentList{}
		return clone
	}
	clone.ExtentList = ds.ExtentList.Clone().Crop(e)
	clone.VolatileExtentList = ds.VolatileExtentList.Clone().Crop(e)

	startNS := epoch.Epoch(e.Start.UnixNano())
	endNS := epoch.Epoch(e.End.UnixNano())

	for i := range ds.Results {
		if ds.Results[i] == nil {
			continue
		}
		clone.Results[i] = &Result{
			StatementID: ds.Results[i].StatementID,
			Error:       ds.Results[i].Error,
		}
		clone.Results[i].SeriesList = make([]*Series, len(ds.Results[i].SeriesList))
		var wg sync.WaitGroup
		var skips bool
		for j, s := range ds.Results[i].SeriesList {
			if s == nil || len(s.Points) == 0 {
				skips = true
				continue
			}
			wg.Add(1)
			go func(s2 *Series, n, o int) {
				sc := &Series{
					Header: s2.Header.Clone(),
				}
				var start, end, l = 0, -1, len(s2.Points)
				var iwg sync.WaitGroup
				iwg.Add(2)
				go func() {
					start = s2.Points.onOrJustAfter(startNS, 0, l-1)
					iwg.Done()
				}()
				go func() {
					end = s2.Points.onOrJustBefore(endNS, 0, l-1) + 1
					iwg.Done()
				}()
				iwg.Wait()
				if start < l && end <= l && end > start {
					sc.Points = s2.Points.CloneRange(start, end)
					sc.PointSize = sc.Points.Size()
					clone.Results[n].SeriesList[o] = sc
				} else {
					skips = true
				}
				wg.Done()
			}(s, i, j)
		}
		wg.Wait()
		if skips {
			sl := make([]*Series, 0, len(ds.Results[i].SeriesList))
			for _, s := range ds.Results[i].SeriesList {
				if s != nil {
					sl = append(sl, s)
				}
			}
			ds.Results[i].SeriesList = sl
		}
	}
	return clone
}

// Clone returns a new, perfect copy of the DataSet
func (ds *DataSet) Clone() timeseries.Timeseries {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	clone := &DataSet{
		Error:        ds.Error,
		Sorter:       ds.Sorter,
		Merger:       ds.Merger,
		SizeCropper:  ds.SizeCropper,
		RangeCropper: ds.RangeCropper,
		Results:      make([]*Result, len(ds.Results)),
	}
	if ds.TimeRangeQuery != nil {
		clone.TimeRangeQuery = ds.TimeRangeQuery.Clone()
	}

	if ds.ExtentList != nil {
		clone.ExtentList = ds.ExtentList.Clone()
	}

	if ds.VolatileExtentList != nil {
		clone.VolatileExtentList = ds.VolatileExtentList.Clone()
	}

	for i := range ds.Results {
		if ds.Results[i] == nil {
			continue
		}
		clone.Results[i] = ds.Results[i].Clone()
	}
	return clone
}

// Merge merges the provided Timeseries list into the base DataSet
// (in the order provided) and optionally sorts the merged DataSet
// This implementation ignores any Timeseries that are not of type *DataSet
func (ds *DataSet) Merge(sortSeries bool, collection ...timeseries.Timeseries) {
	if ds.Merger != nil {
		ds.Merger(sortSeries, collection...)
		return
	}
	ds.DefaultMerger(sortSeries, collection...)
}

// DefaultMerger is the default Merger function
func (ds *DataSet) DefaultMerger(sortSeries bool, collection ...timeseries.Timeseries) {

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	sl := make(SeriesLookup)
	rl := make(ResultsLookup)
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		rl[r.StatementID] = r
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			sl[SeriesLookupKey{StatementID: r.StatementID, Hash: s.Header.CalculateHash()}] = s
		}
	}

	for _, ts := range collection {
		if ts == nil {
			continue
		}
		ds2, ok := ts.(*DataSet)
		if !ok {
			continue
		}
		var rmtx sync.RWMutex
		var rwg sync.WaitGroup
		for _, r := range ds2.Results {
			if r == nil {
				continue
			}
			rmtx.RLock()
			r1, ok := rl[r.StatementID]
			rmtx.RUnlock()
			if !ok {
				rmtx.Lock()
				if _, ok = rl[r.StatementID]; !ok {
					rl[r.StatementID] = r
					ds.Results = append(ds.Results, r)
					for _, s := range r.SeriesList {
						if s == nil {
							continue
						}
						sl[SeriesLookupKey{StatementID: r.StatementID, Hash: s.Header.CalculateHash()}] = s
					}
				}
				rmtx.Unlock()
				continue
			}

			rwg.Add(1)
			var slmtx sync.RWMutex

			// this iterates the new result and appends any new datapoints to pre-existing series
			go func(gr1, gr *Result) {
				var wg sync.WaitGroup

				defer rwg.Done()

				for _, s := range gr.SeriesList {
					if s == nil || len(s.Points) == 0 {
						continue
					}
					wg.Add(1)
					// this checks each series for new entries, and adds them to the main lookup if non-existing
					// or appends new points, if any, to the pre-existing series.
					go func(gs *Series, ggr1 *Result) {
						defer wg.Done()
						var es *Series
						key := SeriesLookupKey{StatementID: ggr1.StatementID, Hash: gs.Header.CalculateHash()}
						slmtx.RLock()
						es, ok = sl[key]
						slmtx.RUnlock()
						if !ok && gs != nil {
							slmtx.Lock()
							sl[key] = gs
							slmtx.Unlock()
							return
						}
						if gs == nil {
							return
						}
						// otherwise, we append points
						es.Points = append(es.Points, gs.Points...)
						// This will sort and dupe kill the list of points, keeping the newest version
						if sortSeries {
							n := len(es.Points)
							sort.Sort(es.Points)
							if n <= 1 {
								// extra 10 capacity prevents an extra copy/expand of the whole
								// slice for small incremental merges on the next load
								es.Points = es.Points.CloneRange(0, n)
							} else {
								x := make(Points, 0, len(es.Points)+10)
								for k := 0; k < n; k++ {
									if k+1 == n || es.Points[k].Epoch != es.Points[k+1].Epoch {
										x = append(x, es.Points[k])
									}
								}
								es.Points = x
							}
						}
						es.PointSize = es.Points.Size()
					}(s, gr1)
				}
				wg.Wait()
			}(r1, r)
			rwg.Wait()
			if len(r.SeriesList) > 0 {
				// if we ended up having any new series, this will actually merge them into
				// the existing result set in the best guess as to the correct location
				r1.SeriesList = SeriesList(r1.SeriesList).merge(r.SeriesList)
			}
			ds.ExtentList = append(ds.ExtentList, ds2.ExtentList...)
		}
	}
	ds.ExtentList = ds.ExtentList.Compress(ds.Step())
}

// CropToSize reduces the number of elements in the Timeseries to the provided count, by evicting elements
// using a least-recently-used methodology. The time parameter limits the upper extent to the provided time,
// in order to support backfill tolerance
func (ds *DataSet) CropToSize(sz int, t time.Time, lur timeseries.Extent) {
	if ds.SizeCropper != nil {
		ds.SizeCropper(sz, t, lur)
		return
	}
	ds.DefaultSizeCropper(sz, t, lur)
}

// DefaultSizeCropper is the default SizeCropper Function
func (ds *DataSet) DefaultSizeCropper(sz int, t time.Time, lur timeseries.Extent) {
	// TODO: Complete this method
}

// CropToRange reduces the DataSet down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base DataSet is already sorted, and will corrupt an unsorted DataSet
func (ds *DataSet) CropToRange(e timeseries.Extent) {
	if ds.RangeCropper != nil {
		ds.RangeCropper(e)
		return
	}
	ds.DefaultRangeCropper(e)
}

// DefaultRangeCropper is the default RangeCropper Function
func (ds *DataSet) DefaultRangeCropper(e timeseries.Extent) {
	x := len(ds.ExtentList)
	// The DataSet has no extents, so no need to do anything
	if x == 0 {
		for i := range ds.Results {
			if ds.Results[i] == nil {
				continue
			}
			ds.Results[i].SeriesList = make([]*Series, 0)
		}
		ds.ExtentList = timeseries.ExtentList{}
		return
	}
	// if the extent of the series is entirely outside the extent of the crop
	// range, return empty set and bail
	if ds.ExtentList.OutsideOf(e) {
		for i := range ds.Results {
			if ds.Results[i] == nil {
				continue
			}
			ds.Results[i].SeriesList = make([]*Series, 0)
		}
		ds.ExtentList = timeseries.ExtentList{}
		return
	}

	ds.VolatileExtentList = ds.VolatileExtentList.Clone().Crop(e)

	// if the series extent is entirely inside the extent of the crop range,
	// simply adjust down its ExtentList
	if ds.ExtentList.EncompassedBy(e) {
		if ds.ValueCount() == 0 {
			for i := range ds.Results {
				ds.Results[i].SeriesList = make([]*Series, 0)
			}
		}
		ds.ExtentList = ds.ExtentList.Crop(e)
		return
	}

	ds.ExtentList = ds.ExtentList.Crop(e)
	startNS := epoch.Epoch(e.Start.UnixNano())
	endNS := epoch.Epoch(e.End.UnixNano())

	for i := range ds.Results {
		if ds.Results[i] == nil {
			continue
		}
		var wg sync.WaitGroup
		if len(ds.Results[i].SeriesList) == 0 {
			continue
		}
		sl := make([]*Series, 0, len(ds.Results[i].SeriesList))
		for _, s := range ds.Results[i].SeriesList {
			if s == nil || len(s.Points) == 0 {
				continue
			}
			wg.Add(1)
			go func(s2 *Series) {
				var start, end, l = 0, -1, len(s2.Points)
				var iwg sync.WaitGroup
				iwg.Add(2)
				go func() {
					start = s2.Points.onOrJustAfter(startNS, 0, l-1)
					iwg.Done()
				}()
				go func() {
					end = s2.Points.onOrJustBefore(endNS, 0, l-1) + 1
					iwg.Done()
				}()
				iwg.Wait()
				if start < l && end <= l && end > start {
					s2.Points = s2.Points.CloneRange(start, end)
					s2.PointSize = s2.Points.Size()
				}
				wg.Done()
				sl = append(sl, s2)
			}(s)
		}
		wg.Wait()
		ds.Results[i].SeriesList = sl
	}
}

// SeriesCount returns the count of all Series across all Results in the DataSet
func (ds *DataSet) SeriesCount() int {
	var cnt int
	for i := range ds.Results {
		if ds.Results[i] == nil {
			continue
		}
		cnt += len(ds.Results[i].SeriesList)
	}
	return cnt
}

// ValueCount returns the count of all values across all Series in the DataSet
func (ds *DataSet) ValueCount() int64 {
	var cnt int64
	for i := range ds.Results {
		if ds.Results[i] == nil {
			continue
		}
		if len(ds.Results[i].SeriesList) == 0 {
			continue
		}
		for _, s := range ds.Results[i].SeriesList {
			if s == nil {
				continue
			}
			cnt += int64(len(s.Points))
		}
	}
	return cnt
}

// Size returns the memory utilization in bytes of the DataSet
func (ds *DataSet) Size() int64 {
	c := int64(len(ds.Status) +
		49 + // StepDuration=8 Mutex=8 OutputFormat=1 4xFuncs=32
		(len(ds.ExtentList) * 72) +
		len(ds.Error))
	for i := range ds.Results {
		c += int64(ds.Results[i].Size())
	}
	return c
}

// SetTimeRangeQuery sets the TimeRangeQuery for the DataSet
func (ds *DataSet) SetTimeRangeQuery(trq *timeseries.TimeRangeQuery) {
	ds.TimeRangeQuery = trq
}

// Step returns the step for the DataSet
func (ds *DataSet) Step() time.Duration {
	if ds.TimeRangeQuery != nil {
		return ds.TimeRangeQuery.Step
	}
	return 0
}

// TimestampCount returns the count of unique timestamps across all series in the DataSet
func (ds *DataSet) TimestampCount() int64 {
	return ds.ExtentList.TimestampCount(ds.Step())
}

// Extents returns the DataSet's ExentList
func (ds *DataSet) Extents() timeseries.ExtentList {
	return ds.ExtentList
}

// SetExtents overwrites a DataSet's known extents with the provided extent list
func (ds *DataSet) SetExtents(el timeseries.ExtentList) {
	if el != nil {
		ds.ExtentList = el.Clone()
	}
}

// Sort sorts all Values in each Series chronologically by their timestamp
// Sorting is efficiently baked into DataSet.Merge(), therefore this interface function is unused
// unless overridden
func (ds *DataSet) Sort() {
	if ds.Sorter != nil {
		ds.Sorter()
		return
	}
}

// UnmarshalDataSet unmarshals the dataset from a msgpack-formatted byte slice
func UnmarshalDataSet(b []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	ds := &DataSet{}
	_, err := ds.UnmarshalMsg(b)
	if err == nil {
		if ds.TimeRangeQuery != nil {
			ds.TimeRangeQuery.Step = time.Duration(ds.TimeRangeQuery.StepNS)
		} else {
			ds.TimeRangeQuery = trq
		}
	}
	return ds, err
}

// MarshalDataSet marshals the dataset into a msgpack-formatted byte slice
func MarshalDataSet(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	ds, ok := ts.(*DataSet)
	if !ok || ds == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	if ds.TimeRangeQuery != nil {
		ds.TimeRangeQuery.StepNS = ds.TimeRangeQuery.Step.Nanoseconds()
	}
	return ds.MarshalMsg(nil)
}

// VolatileExtents returns the list of time Extents in the dataset that should be re-fetched
func (ds *DataSet) VolatileExtents() timeseries.ExtentList {
	return ds.VolatileExtentList
}

// SetVolatileExtents sets the list of time Extents in the dataset that should be re-fetched
func (ds *DataSet) SetVolatileExtents(e timeseries.ExtentList) {
	ds.VolatileExtentList = e
}
