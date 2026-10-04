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

package dataset

import (
	"math"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// The functions in this file never modify their inputs, and their results share points and values
// with them, so a cached DataSet can be read by any number of requests at once.

// Based is implemented by a DataSet, and so by any provider model that embeds one.
type Based interface {
	Base() *DataSet
}

// Base returns the DataSet, which a provider model embedding it may extend.
func (ds *DataSet) Base() *DataSet {
	return ds
}

// View returns ds's rows within the inclusive extent e, sharing ds's memory.
func (ds *DataSet) View(e timeseries.Extent) *DataSet {
	out := &DataSet{
		TimeRangeQuery: ds.TimeRangeQuery, ExtentList: ds.ExtentList.Crop(e),
		Results: make(Results, 0, len(ds.Results)),
	}
	start, end := epoch.Epoch(e.Start.UnixNano()), epoch.Epoch(e.End.UnixNano())
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		vr := &Result{
			StatementID: r.StatementID, Name: r.Name, Error: r.Error,
			SeriesList: make(SeriesList, 0, len(r.SeriesList)),
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			if v := s.segs.View(start, end); v.Len() > 0 {
				vr.SeriesList = append(vr.SeriesList, viewSeries(s, v))
			}
		}
		out.Results = append(out.Results, vr)
	}
	return out
}

// FullView returns what Clone would, sharing ds's read-only rows and tags; the rest is its own, so
// merging into, cropping or re-extenting it leaves ds as is
func (ds *DataSet) FullView() *DataSet {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	out := ds.viewEnvelope()
	if ds.ExtentList != nil {
		out.ExtentList = ds.ExtentList.Clone()
	}
	if ds.VolatileExtentList != nil {
		out.VolatileExtentList = ds.VolatileExtentList.Clone()
	}
	out.Results = make(Results, 0, len(ds.Results))
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		vr := &Result{
			Name: r.Name, StatementID: r.StatementID, Error: r.Error,
			SeriesList: make(SeriesList, len(r.SeriesList)),
		}
		for i, s := range r.SeriesList {
			if s != nil {
				vr.SeriesList[i] = &Series{Header: s.Header, segs: slices.Clip(s.segs)}
			}
		}
		out.Results = append(out.Results, vr)
	}
	return out
}

// CroppedView returns the DataSet CroppedClone would, sharing ds's rows as FullView does
func (ds *DataSet) CroppedView(e timeseries.Extent) *DataSet {
	if len(ds.ExtentList) == 0 || ds.Results == nil {
		return ds.FullView()
	}
	if ds.ExtentList.EncompassedBy(e) {
		out := ds.FullView()
		out.ExtentList = out.ExtentList.Crop(e)
		return out
	}
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	out := ds.viewEnvelope()
	out.Results = make(Results, len(ds.Results))
	if ds.ExtentList.OutsideOf(e) {
		for i, r := range ds.Results {
			if r != nil {
				out.Results[i] = &Result{StatementID: r.StatementID, Error: r.Error, SeriesList: make(SeriesList, 0)}
			}
		}
		out.ExtentList = timeseries.ExtentList{}
		return out
	}
	out.ExtentList = ds.ExtentList.Crop(e)
	out.VolatileExtentList = ds.VolatileExtentList.Crop(e)
	start, end := epoch.Epoch(e.Start.UnixNano()), epoch.Epoch(e.End.UnixNano())
	for i, r := range ds.Results {
		if r == nil {
			continue
		}
		vr := &Result{StatementID: r.StatementID, Error: r.Error, SeriesList: make(SeriesList, 0, len(r.SeriesList))}
		for _, s := range r.SeriesList {
			if s == nil || s.PointCount() == 0 {
				continue
			}
			if v := s.segs.View(start, end); v.Len() > 0 {
				vr.SeriesList = append(vr.SeriesList, &Series{Header: s.Header, segs: v})
			}
		}
		out.Results[i] = vr
	}
	return out
}

// the fields Clone and CroppedClone carry over besides the data; the caller holds ds.UpdateLock
func (ds *DataSet) viewEnvelope() *DataSet {
	return &DataSet{
		SourceResultType: ds.SourceResultType,
		Error:            ds.Error,
		TimeRangeQuery:   ds.TimeRangeQuery,
		Sorter:           ds.Sorter,
		Merger:           ds.Merger,
		SizeCropper:      ds.SizeCropper,
		RangeCropper:     ds.RangeCropper,
		ValueOperations:  ds.ValueOperations,
	}
}

func viewSeries(s *Series, v Segments) *Series {
	if len(v) == len(s.segs) && v.Len() == s.segs.Len() {
		return s
	}
	return &Series{Header: s.Header, segs: v}
}

// MergeDisjoint returns the parts' rows matched into series by header, shared until a series spans over
// maxStoredSegments. Series must be sorted; where parts share an epoch, the later part's row wins.
func MergeDisjoint(trq *timeseries.TimeRangeQuery, parts ...*DataSet) *DataSet {
	var step time.Duration
	if trq != nil {
		step = trq.Step
	}
	out := MergeDisjointStep(step, parts...)
	out.TimeRangeQuery = trq
	return out
}

// MergeDisjointStep is MergeDisjoint for a caller with no TimeRangeQuery for the result to carry;
// the parts' extents coalesce at step
func MergeDisjointStep(step time.Duration, parts ...*DataSet) *DataSet {
	return mergeDisjoint(step, false, parts)
}

// MergeDisjointParts is MergeDisjointStep for a response, which is never stored: a series' time-ordered
// lists are joined as Segments without copying their rows however many there are
func MergeDisjointParts(step time.Duration, parts ...*DataSet) *DataSet {
	return mergeDisjoint(step, true, parts)
}

func mergeDisjoint(step time.Duration, withParts bool, parts []*DataSet) *DataSet {
	out := &DataSet{}
	// the bookkeeping for a merged series, which holds its first few parts' rows inline
	type merging struct {
		s       *Series
		lists   []Segments
		listBuf [3]Segments
	}
	var free []merging
	results := map[int]*Result{}
	indexes := map[int]*seriesIndex[*merging]{}
	var order []*merging
	for _, part := range parts {
		if part == nil {
			continue
		}
		out.ExtentList = out.ExtentList.Merge(part.ExtentList, step)
		for _, r := range part.Results {
			if r == nil {
				continue
			}
			mr, ok := results[r.StatementID]
			if !ok {
				mr = &Result{StatementID: r.StatementID, Name: r.Name, Error: r.Error}
				results[r.StatementID] = mr
				indexes[r.StatementID] = newSeriesIndex(len(r.SeriesList), func(m *merging) *SeriesHeader {
					return &m.s.Header
				})
				out.Results = append(out.Results, mr)
			}
			index := indexes[r.StatementID]
			for i, s := range r.SeriesList {
				if s == nil || s.PointCount() == 0 {
					continue
				}
				hash := s.Header.CalculateHashWithQueryStatement(s.Header.QueryStatement)
				m, found := index.find(hash, &s.Header)
				if !found {
					if len(free) == 0 {
						// the first part's series are all new; a later part's are mostly merged already
						n := len(r.SeriesList) - i
						if len(order) > 0 {
							n = min(n, maxMergingChunk)
						}
						free = make([]merging, n)
					}
					m, free = &free[0], free[1:]
					m.s = &Series{Header: s.Header}
					m.lists = m.listBuf[:0]
					index.add(hash, m)
					mr.SeriesList = append(mr.SeriesList, m.s)
					order = append(order, m)
				}
				m.lists = append(m.lists, s.segs)
			}
		}
	}
	maxSegments := maxStoredSegments
	if withParts {
		maxSegments = math.MaxInt
	}
	for _, m := range order {
		m.s.segs = JoinSegments(m.lists, maxSegments)
	}
	return out
}

// the most Segments a merged series may span before its rows are copied into one; a cache that stores
// the merge each time it adds rows so copies them only every few merges
const maxStoredSegments = 8

// the most merge records a later part allocates at once, for series it adds
const maxMergingChunk = 64

// RetainNewest returns a view of ds's newest n epochs across all its series, which must be sorted,
// and the oldest epoch kept; retained is false when ds has no more than n epochs
func (ds *DataSet) RetainNewest(n int) (view *DataSet, oldest time.Time, retained bool) {
	if n <= 0 {
		return ds, time.Time{}, false
	}
	points := 0
	var lists []Segments
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil {
				points += s.PointCount()
				lists = append(lists, s.segs)
			}
		}
	}
	if points <= n {
		// there can't be more epochs than points
		return ds, time.Time{}, false
	}
	at, ok := NewestEpoch(lists, n)
	if !ok {
		return ds, time.Time{}, false
	}
	oldest = time.Unix(0, int64(at))
	// the view runs open-ended from the oldest epoch kept, so coverage past the newest point stays
	return ds.View(timeseries.Extent{Start: oldest, End: time.Unix(0, math.MaxInt64)}), oldest, true
}
