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

package influxdb

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tricksterproxy/trickster/pkg/sort/times"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	str "github.com/tricksterproxy/trickster/pkg/util/strings"

	"github.com/influxdata/influxdb/models"
)

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (se *SeriesEnvelope) SetExtents(extents timeseries.ExtentList) {
	el := make(timeseries.ExtentList, len(extents))
	copy(el, extents)
	se.ExtentList = el
	se.isCounted = false
}

// Extents returns the Timeseries's ExentList
func (se *SeriesEnvelope) Extents() timeseries.ExtentList {
	return se.ExtentList
}

// ValueCount returns the count of all values across all series in the Timeseries
func (se *SeriesEnvelope) ValueCount() int {
	c := 0
	for i := range se.Results {
		for j := range se.Results[i].Series {
			c += len(se.Results[i].Series[j].Values)
		}
	}
	return c
}

// TimestampCount returns the count unique timestampes in across all series in the Timeseries
func (se *SeriesEnvelope) TimestampCount() int {
	if se.timestamps == nil {
		se.timestamps = make(map[time.Time]bool)
	}
	se.updateTimestamps()
	return len(se.timestamps)
}

func (se *SeriesEnvelope) updateTimestamps() {
	if se.isCounted {
		return
	}
	m := make(map[time.Time]bool)

	for i := range se.Results {
		for j, s := range se.Results[i].Series {
			ti := str.IndexOfString(s.Columns, "time")
			if ti < 0 {
				continue
			}
			for k := range se.Results[i].Series[j].Values {
				m[time.Unix(int64(se.Results[i].Series[j].Values[k][ti].(float64)/1000), 0)] = true
			}
		}
	}

	se.timestamps = m
	se.tslist = times.FromMap(m)
	se.isCounted = true
}

// SeriesCount returns the count of all Results in the Timeseries
// it is called SeriesCount due to Interface conformity and the disparity in nomenclature between various TSDBs.
func (se *SeriesEnvelope) SeriesCount() int {
	return len(se.Results)
}

// Step returns the step for the Timeseries
func (se *SeriesEnvelope) Step() time.Duration {
	return se.StepDuration
}

// SetStep sets the step for the Timeseries
func (se *SeriesEnvelope) SetStep(step time.Duration) {
	se.StepDuration = step
}

type seriesKey struct {
	ResultID    int
	StatementID int
	Name        string
	Tags        string
	Columns     string
}

type tags map[string]string

func (t tags) String() string {
	if len(t) == 0 {
		return ""
	}
	pairs := make(sort.StringSlice, len(t))
	var i int
	for k, v := range t {
		pairs[i] = fmt.Sprintf("%s=%s", k, v)
		i++
	}
	sort.Sort(pairs)
	return strings.Join(pairs, ";")
}

// Merge merges the provided Timeseries list into the base Timeseries
// (in the order provided) and optionally sorts the merged Timeseries
func (se *SeriesEnvelope) Merge(sort bool, collection ...timeseries.Timeseries) {

	mtx := sync.Mutex{}
	wg := sync.WaitGroup{}

	se.updateLock.Lock()
	defer se.updateLock.Unlock()

	series := make(map[seriesKey]*models.Row)
	for i, r := range se.Results {
		for j := range se.Results[i].Series {
			wg.Add(1)
			go func(s *models.Row) {
				mtx.Lock()
				series[seriesKey{ResultID: i, StatementID: r.StatementID, Name: s.Name,
					Tags: tags(s.Tags).String(), Columns: strings.Join(s.Columns, ",")}] = s
				mtx.Unlock()
				wg.Done()
			}(&se.Results[i].Series[j])
		}
	}
	wg.Wait()

	for _, ts := range collection {
		if ts != nil {
			se2 := ts.(*SeriesEnvelope)
			for g, r := range se2.Results {

				if g >= len(se.Results) {
					mtx.Lock()
					se.Results = append(se.Results, se2.Results[g:]...)
					mtx.Unlock()
					break
				}

				for i := range r.Series {
					wg.Add(1)
					go func(s *models.Row, resultID int) {
						mtx.Lock()
						sk := seriesKey{ResultID: g, StatementID: r.StatementID, Name: s.Name,
							Tags: tags(s.Tags).String(), Columns: strings.Join(s.Columns, ",")}
						if _, ok := series[sk]; !ok {
							series[sk] = s
							se.Results[resultID].Series = append(se.Results[resultID].Series, *s)
							mtx.Unlock()
							wg.Done()
							return
						}
						series[sk].Values = append(series[sk].Values, s.Values...)
						mtx.Unlock()
						wg.Done()
					}(&r.Series[i], g)
				}
			}
			wg.Wait()
			mtx.Lock()
			se.ExtentList = append(se.ExtentList, se2.ExtentList...)
			mtx.Unlock()
		}
	}

	se.ExtentList = se.ExtentList.Compress(se.StepDuration)
	se.isSorted = false
	se.isCounted = false
	if sort {
		se.Sort()
	}
}

// Clone returns a perfect copy of the base Timeseries
func (se *SeriesEnvelope) Clone() timeseries.Timeseries {
	se.updateLock.Lock()
	defer se.updateLock.Unlock()
	clone := &SeriesEnvelope{
		Err:          se.Err,
		Results:      make([]Result, len(se.Results)),
		StepDuration: se.StepDuration,
		ExtentList:   make(timeseries.ExtentList, len(se.ExtentList)),
		timestamps:   make(map[time.Time]bool),
		tslist:       make(times.Times, len(se.tslist)),
		isCounted:    se.isCounted,
		isSorted:     se.isSorted,
	}
	copy(clone.ExtentList, se.ExtentList)
	for k, v := range se.timestamps {
		clone.timestamps[k] = v
	}
	copy(clone.tslist, se.tslist)
	for i, r := range se.Results {
		nres := Result{
			Series: make([]models.Row, len(r.Series)),
		}
		nres.Err = r.Err
		nres.StatementID = r.StatementID
		for l, row := range r.Series {
			nrow := models.Row{
				Name:    row.Name,
				Partial: row.Partial,
				Columns: make([]string, len(row.Columns)),
				Tags:    make(map[string]string),
				Values:  make([][]interface{}, len(row.Values)),
			}
			copy(nrow.Columns, row.Columns)
			for k, v := range row.Tags {
				nrow.Tags[k] = v
			}
			for j := range row.Values {
				nrow.Values[j] = make([]interface{}, len(row.Values[j]))
				copy(nrow.Values[j], row.Values[j])
			}
			nres.Series[l] = nrow
		}
		clone.Results[i] = nres
	}
	return clone
}

// CropToSize reduces the number of elements in the Timeseries to the provided count, by evicting elements
// using a least-recently-used methodology. The time parameter limits the upper extent to the provided time,
// in order to support backfill tolerance
func (se *SeriesEnvelope) CropToSize(sz int, t time.Time, lur timeseries.Extent) {

	se.isCounted = false
	se.isSorted = false
	x := len(se.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		for i := range se.Results {
			se.Results[i].Series = []models.Row{}
		}
		se.ExtentList = timeseries.ExtentList{}
		return
	}

	// Crop to the Backfill Tolerance Value if needed
	if se.ExtentList[x-1].End.After(t) {
		se.CropToRange(timeseries.Extent{Start: se.ExtentList[0].Start, End: t})
	}

	tc := se.TimestampCount()
	if len(se.Results) == 0 || tc <= sz {
		return
	}

	el := timeseries.ExtentListLRU(se.ExtentList).UpdateLastUsed(lur, se.StepDuration)
	sort.Sort(el)

	rc := tc - sz // # of required timestamps we must delete to meet the rentention policy
	removals := make(map[time.Time]bool)
	done := false
	var ok bool

	for _, x := range el {
		for ts := x.Start; !x.End.Before(ts) && !done; ts = ts.Add(se.StepDuration) {
			if _, ok = se.timestamps[ts]; ok {
				removals[ts] = true
				done = len(removals) >= rc
			}
		}
		if done {
			break
		}
	}

	ti := str.IndexOfString(se.Results[0].Series[0].Columns, "time")

	for i, r := range se.Results {
		for j, s := range r.Series {
			tmp := se.Results[i].Series[j].Values[:0]
			for _, v := range se.Results[i].Series[j].Values {
				t = time.Unix(int64(v[ti].(float64)/1000), 0)
				if _, ok := removals[t]; !ok {
					tmp = append(tmp, v)
				}
			}
			s.Values = tmp
		}
	}

	tl := times.FromMap(removals)
	sort.Sort(tl)
	for _, t := range tl {
		for i, e := range el {
			if e.StartsAt(t) {
				el[i].Start = e.Start.Add(se.StepDuration)
			}
		}
	}

	se.ExtentList = timeseries.ExtentList(el).Compress(se.StepDuration)
	se.Sort()
}

// CropToRange reduces the Timeseries down to timestamps contained within the provided Extents (inclusive).
// CropToRange assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (se *SeriesEnvelope) CropToRange(e timeseries.Extent) {
	se.isCounted = false
	x := len(se.ExtentList)
	// The Series has no extents, so no need to do anything
	if x < 1 {
		for i := range se.Results {
			se.Results[i].Series = []models.Row{}
		}
		se.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the extent of the series is entirely outside the extent of the crop range, return empty set and bail
	if se.ExtentList.OutsideOf(e) {
		for i := range se.Results {
			se.Results[i].Series = []models.Row{}
		}
		se.ExtentList = timeseries.ExtentList{}
		return
	}

	// if the series extent is entirely inside the extent of the crop range, simple adjust down its ExtentList
	if se.ExtentList.InsideOf(e) {
		if se.ValueCount() == 0 {
			for i := range se.Results {
				se.Results[i].Series = []models.Row{}
			}
		}
		se.ExtentList = se.ExtentList.Crop(e)
		return
	}

	startSecs := e.Start.Unix()
	endSecs := e.End.Unix()

	for i, r := range se.Results {

		if len(r.Series) == 0 {
			se.ExtentList = se.ExtentList.Crop(e)
			continue
		}

		deletes := make(map[int]bool)

		for j, s := range r.Series {
			// check the index of the time column again just in case it changed in the next series
			ti := str.IndexOfString(s.Columns, "time")
			if ti != -1 {
				start := -1
				end := -1
				for vi, v := range se.Results[i].Series[j].Values {
					t := int64(v[ti].(float64) / 1000)
					if t == endSecs {
						if vi == 0 || t == startSecs || start == -1 {
							start = vi
						}
						end = vi + 1
						break
					}
					if t > endSecs {
						end = vi
						break
					}
					if t < startSecs {
						continue
					}
					if start == -1 && (t == startSecs || (endSecs > t && t > startSecs)) {
						start = vi
					}
				}
				if start != -1 {
					if end == -1 {
						end = len(s.Values)
					}
					se.Results[i].Series[j].Values = s.Values[start:end]
				} else {
					deletes[j] = true
				}
			}
		}
		if len(deletes) > 0 {
			tmp := se.Results[i].Series[:0]
			for i, r := range se.Results[i].Series {
				if _, ok := deletes[i]; !ok {
					tmp = append(tmp, r)
				}
			}
			se.Results[i].Series = tmp
		}
	}
	se.ExtentList = se.ExtentList.Crop(e)
}

// Sort sorts all Values in each Series chronologically by their timestamp
func (se *SeriesEnvelope) Sort() {

	if se.isSorted || len(se.Results) == 0 {
		return
	}

	mtx := sync.Mutex{}

	var hasWarned bool
	tsm := map[time.Time]bool{}
	for ri := range se.Results {
		seriesWG := sync.WaitGroup{}
		for si := range se.Results[ri].Series {
			seriesWG.Add(1)
			go func(j int) {
				wg := sync.WaitGroup{}
				tsLookup := make(map[int64][]interface{})
				timestamps := make([]int64, 0, len(se.Results[ri].Series[j].Values))
				for _, v := range se.Results[ri].Series[j].Values {
					wg.Add(1)
					go func(s []interface{}) {
						defer wg.Done()
						if len(s) == 0 {
							return
						}
						if tf, ok := s[0].(float64); ok {
							t := int64(tf)
							mtx.Lock()
							if _, ok := tsLookup[t]; !ok {
								timestamps = append(timestamps, t)
								tsLookup[t] = s
							}
							tsm[time.Unix(t/1000, 0)] = true
							mtx.Unlock()
						} else if !hasWarned {
							hasWarned = true
							// this makeshift warning is temporary during the beta cycle to help
							// troubleshoot #433
							fmt.Println("WARN", "could not convert influxdb time to a float64:",
								s[0], "resultSet:", se)
						}
					}(v)
				}
				wg.Wait()
				sort.Slice(timestamps, func(i, j int) bool {
					return timestamps[i] < timestamps[j]
				})
				sm := make([][]interface{}, len(timestamps))
				for i, key := range timestamps {
					sm[i] = tsLookup[key]
				}
				se.Results[ri].Series[j].Values = sm
				seriesWG.Done()
			}(si)
		}
		seriesWG.Wait()
	}

	sort.Sort(se.ExtentList)

	se.timestamps = tsm
	se.tslist = times.FromMap(tsm)
	se.isCounted = true
	se.isSorted = true
}

// Size returns the approximate memory utilization in bytes of the timeseries
func (se *SeriesEnvelope) Size() int {
	c := uint64(24 + // .stepDuration
		len(se.Err) +
		se.ExtentList.Size() + // time.Time (24) * 3
		(25 * len(se.timestamps)) + // time.Time (24) + bool(1)
		(24 * len(se.tslist)) + // time.Time (24)
		2, // .isSorted + .isCounted
	)
	wg := sync.WaitGroup{}
	for i, res := range se.Results {
		atomic.AddUint64(&c, uint64(8+len(res.Err))) // .StatementID
		for j := range res.Series {
			wg.Add(1)
			go func(r models.Row) {
				atomic.AddUint64(&c, uint64(len(r.Name)+1)) // .Partial
				for k, v := range r.Tags {
					atomic.AddUint64(&c, uint64(len(k)+len(v)))
				}
				for _, v := range r.Columns {
					atomic.AddUint64(&c, uint64(len(v)))
				}
				atomic.AddUint64(&c, 32) // size of timestamp (24) + approximate value size (8)
				wg.Done()
			}(se.Results[i].Series[j])
		}
	}
	wg.Wait()
	return int(c)
}
