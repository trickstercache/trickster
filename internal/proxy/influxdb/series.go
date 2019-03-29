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

package influxdb

import (
	"sort"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/influxdata/influxdb/models"
)

const (
	// MinimumTick is the minimum supported time resolution. This has to be
	// at least time.Second in order for the code below to work.
	minimumTick = time.Millisecond
	// milliSecondValue is the no of milliseconds in one second (1000).
	milliSecondValue = int64(time.Second / minimumTick)
)

func contains(arr []string, key string) int {
	for i := 0; i < len(arr); i++ {
		element := arr[i]
		if element == key {
			return i
		}
	}
	return -1
}

// SetExtents ...
func (se SeriesEnvelope) SetExtents(extents []timeseries.Extent) {
	for i := 0; i < len(extents); i++ {
		se.ExtentList[i] = extents[i]
	}
}

// Extremes ...
func (se *SeriesEnvelope) Extremes() []timeseries.Extent {

	var containsIndex = contains(se.Results[0].Series[0].Columns, "time")
	var ans []int64
	if containsIndex != -1 {
		resultSize := len(se.Results)
		for index := 0; index < resultSize; index++ {
			seriesSize := len(se.Results[index].Series)
			for seriesIndex := 0; seriesIndex < seriesSize; seriesIndex++ {
				for _, v := range se.Results[index].Series[seriesIndex].Values {
					if ts, ok := v[0].(time.Time); ok {
						ans = append(ans, ts.UnixNano())
					}
				}
			}
		}
		max := ans[0]
		min := ans[0]
		for i := 1; i < len(ans); i++ {
			if ans[i] > max {
				max = ans[i]
			}
			if ans[i] < min {
				min = ans[i]
			}
		}
		se.ExtentList = []timeseries.Extent{timeseries.Extent{Start: time.Unix(min/milliSecondValue, (min%milliSecondValue)*(int64(minimumTick))), End: time.Unix(max/milliSecondValue, (max%milliSecondValue)*(int64(minimumTick)))}}
		return se.ExtentList
	}
	return nil
}

// Extents ...
func (se SeriesEnvelope) Extents() []timeseries.Extent {
	if len(se.Extents()) == 0 {
		return se.Extremes()
	}
	return se.ExtentList
}

// CalculateDeltas ...
//func (se SeriesEnvelope) CalculateDeltas(trq *timeseries.TimeRangeQuery) []timeseries.Extent {
//	se.Extremes()
//	misses := []time.Time{}
//	for i := trq.Extent.Start; trq.Extent.End.After(i) || trq.Extent.End == i; i = i.Add(time.Second * time.Duration(trq.Step)) {
//		found := false
//		for j := range se.Extents() {
//			if i == se.Extents()[j].Start || i == se.Extents()[j].End || (i.After(se.Extents()[j].Start) && se.Extents()[j].End.After(i)) {
//				found = true
//				break
//			}
//		}
//		if !found {
//			misses = append(misses, i)
//		}
//	}
//	// Find the fill and gap ranges
//	ins := []timeseries.Extent{}
//	e := time.Unix(0, 0)
//	var inStart = e
//	l := len(misses)
//	for i := range misses {
//		if inStart == e {
//			inStart = misses[i]
//		}
//		if i+1 == l || misses[i+1] != misses[i].Add(se.Step()) {
//			ins = append(ins, timeseries.Extent{Start: inStart, End: misses[i]})
//			inStart = e
//		}
//	}
//	return ins
//}

// Step ...
func (se SeriesEnvelope) Step() time.Duration {
	return se.StepDuration
}

// SetStep ...
func (se SeriesEnvelope) SetStep(step time.Duration) {
	se.StepDuration = step
}

// Merge ...
func (se SeriesEnvelope) Merge(collection ...timeseries.Timeseries) {
	seResults := make(map[int]*Result)

	for _, s := range se.Results {
		seResults[s.StatementID] = &s
	}

	for _, ts := range collection {
		if ts != nil {
			se2 := ts.(*SeriesEnvelope)
			for _, s := range se2.Results {
				id := s.StatementID
				if o, ok := seResults[id]; !ok {
					seResults[id] = o
					continue
				}
				seResults[id].Series = append(seResults[id].Series, s.Series...)
			}
			se.ExtentList = append(se.ExtentList, se2.ExtentList...)
		}
	}
	se.Sort()
}

// Copy ...
func (se SeriesEnvelope) Copy() timeseries.Timeseries {
	resultSe := &SeriesEnvelope{
		Err:     se.Err,
		Results: make([]Result, 0, len(se.Results)),
	}
	for index := range se.Results {
		resResult := se.Results[index]
		resResult.Err = se.Results[index].Err
		resResult.StatementID = se.Results[index].StatementID
		for seriesIndex := range se.Results[index].Series {
			serResult := se.Results[index].Series[seriesIndex]
			serResult.Name = se.Results[index].Series[seriesIndex].Name
			serResult.Partial = se.Results[index].Series[seriesIndex].Partial

			serResult.Columns = make([]string, len(se.Results[index].Series[seriesIndex].Columns))
			copy(serResult.Columns, se.Results[index].Series[seriesIndex].Columns)

			serResult.Tags = make(map[string]string)

			// Copy from the original map to the target map
			for key, value := range se.Results[index].Series[seriesIndex].Tags {
				serResult.Tags[key] = value
			}

			serResult.Values = make([][]interface{}, len(se.Results[index].Series[seriesIndex].Values))
			for i := range se.Results[index].Series[seriesIndex].Values {
				serResult.Values[i] = make([]interface{}, len(se.Results[index].Series[seriesIndex].Values[i]))
				copy(serResult.Values[i], se.Results[index].Series[seriesIndex].Values[i])
			}

			resResult.Series[seriesIndex] = serResult
		}
		resultSe.Results[index] = resResult
	}
	return resultSe
}

// Crop ...
func (se SeriesEnvelope) Crop(e timeseries.Extent) timeseries.Timeseries {

	ts := &SeriesEnvelope{
		Err:     se.Err,
		Results: make([]Result, 0, len(se.Results)),
	}

	for _, s := range se.Results[0].Series {
		l := len(s.Values)
		ss := &models.Row{Name: s.Name, Tags: s.Tags, Columns: s.Columns, Values: make([][]interface{}, 0, l), Partial: s.Partial}
		start := 0
		end := 0

		for i, val := range s.Values[0] {
			t, _ := val.(time.Time)

			if t.After(e.End) {
				break
			}

			if t.Before(e.Start) {
				continue
			}

			if start == 0 && (t == e.Start || (e.End.After(t) && t.After(e.Start))) {
				start = i
				continue
			}

			if end == 0 && (t == e.End) {
				end = i + 1
			}
		}
		if start > 0 {
			if end == 0 {
				end = len(s.Values[0]) - 1
			}

			ss.Values[0] = s.Values[0][start:end]
		}
		ts.Results[0].Series = append(ts.Results[0].Series, *ss)
	}
	return ts
}

// Sort ...
func (se SeriesEnvelope) Sort() {
	m := make(map[int64]models.Row)
	var containsIndex = contains(se.Results[0].Series[0].Columns, "time")
	if containsIndex != -1 {
		resultSize := len(se.Results)
		for index := 0; index < resultSize; index++ {
			seriesSize := len(se.Results[index].Series)
			for seriesIndex := 0; seriesIndex < seriesSize; seriesIndex++ {
				for _, v := range se.Results[index].Series[seriesIndex].Values {
					if ts, ok := v[0].(time.Time); ok {
						m[ts.UnixNano()] = se.Results[index].Series[seriesIndex]
					}
				}
			}
			keys := make([]int64, 0, len(m))
			for key := range m {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
			sm := make([]models.Row, 0, len(keys))
			for _, key := range keys {
				sm = append(sm, m[key])

			}
			se.Results[index].Series = sm
		}
	}
}
