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
	"sort"
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

// View returns ds's points within the inclusive extent e. The view shares points and values with
// ds, so neither may be modified while the other is in use.
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
			from, to := pointsWithin(s.Points, start, end)
			if from >= to {
				continue
			}
			vr.SeriesList = append(vr.SeriesList, viewSeries(s, s.Points[from:to:to]))
		}
		out.Results = append(out.Results, vr)
	}
	return out
}

func pointsWithin(pts Points, start, end epoch.Epoch) (int, int) {
	from := sort.Search(len(pts), func(i int) bool { return pts[i].Epoch >= start })
	to := from + sort.Search(len(pts)-from, func(i int) bool { return pts[from+i].Epoch > end })
	return from, to
}

func viewSeries(s *Series, pts Points) *Series {
	if len(pts) == len(s.Points) {
		return s
	}
	var size int64
	for i := range pts {
		size += int64(pts[i].Size)
	}
	return &Series{Header: s.Header, Points: pts, PointSize: size}
}

// MergeDisjoint returns the parts' points, matched into series by header and shared with them.
// Series must be sorted; where parts share an epoch, the later part's point wins.
func MergeDisjoint(trq *timeseries.TimeRangeQuery, parts ...*DataSet) *DataSet {
	var step time.Duration
	if trq != nil {
		step = trq.Step
	}
	out := &DataSet{TimeRangeQuery: trq}
	type merging struct {
		s     *Series
		lists []Points
	}
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
			for _, s := range r.SeriesList {
				if s == nil || len(s.Points) == 0 {
					continue
				}
				hash := s.Header.CalculateHashWithQueryStatement(s.Header.QueryStatement)
				m, found := index.find(hash, &s.Header)
				if !found {
					m = &merging{s: &Series{Header: s.Header}}
					index.add(hash, m)
					mr.SeriesList = append(mr.SeriesList, m.s)
					order = append(order, m)
				}
				m.lists = append(m.lists, s.Points)
			}
		}
	}
	for _, m := range order {
		m.s.Points = joinPoints(m.lists)
		for i := range m.s.Points {
			m.s.PointSize += int64(m.s.Points[i].Size)
		}
	}
	return out
}

func joinPoints(lists []Points) Points {
	if len(lists) == 1 {
		return slices.Clip(lists[0])
	}
	n, ordered := 0, true
	for i, pts := range lists {
		n += len(pts)
		if i > 0 && len(lists[i-1]) > 0 && pts[0].Epoch <= lists[i-1][len(lists[i-1])-1].Epoch {
			ordered = false
		}
	}
	out := make(Points, 0, n)
	for _, pts := range lists {
		out = append(out, pts...)
	}
	if ordered {
		return out
	}
	// a stable sort keeps the parts' order among equal epochs, so the later part's point is last
	slices.SortStableFunc(out, pointCmp)
	k := 0
	for i := 1; i < len(out); i++ {
		if out[i].Epoch != out[k].Epoch {
			k++
		}
		out[k] = out[i]
	}
	return out[:k+1]
}

// RetainNewest returns a view of ds's newest n epochs, counted across all series, and the oldest
// epoch kept; retained is false when ds holds no more than n epochs.
func (ds *DataSet) RetainNewest(n int) (view *DataSet, oldest time.Time, retained bool) {
	if n <= 0 {
		return ds, time.Time{}, false
	}
	points := 0
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil {
				points += len(s.Points)
			}
		}
	}
	if points <= n {
		// there can't be more epochs than points
		return ds, time.Time{}, false
	}
	epochs := make([]epoch.Epoch, 0, points)
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			for i := range s.Points {
				epochs = append(epochs, s.Points[i].Epoch)
			}
		}
	}
	slices.Sort(epochs)
	epochs = slices.Compact(epochs)
	if len(epochs) <= n {
		return ds, time.Time{}, false
	}
	oldest = time.Unix(0, int64(epochs[len(epochs)-n]))
	// the view runs open-ended from the oldest epoch kept, so coverage past the newest point stays
	return ds.View(timeseries.Extent{Start: oldest, End: time.Unix(0, math.MaxInt64)}), oldest, true
}
