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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	str "github.com/Comcast/trickster/internal/util/strings"

	"github.com/influxdata/influxdb/models"
)

const (
	// MinimumTick is the minimum supported time resolution. This has to be
	// at least time.Second in order for the code below to work.
	minimumTick = time.Millisecond
	// milliSecondValue is the no of milliseconds in one second (1000).
	milliSecondValue = int64(time.Second / minimumTick)
)

// SetExtents overwrites a Timeseries's known extents with the provided extent list
func (se *SeriesEnvelope) SetExtents(extents timeseries.ExtentList) {
	se.ExtentList = make(timeseries.ExtentList, len(extents))
	copy(se.ExtentList, extents)
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
	StatementID int
	Name        string
	Tags        string
	Columns     string
}

type tags map[string]string

func (t tags) String() string {
	var pairs string
	for k, v := range t {
		pairs += fmt.Sprintf("%s=%s;", k, v)
	}
	return pairs
}

// Merge merges the provided Timeseries list into the base Timeseries (in the order provided) and optionally sorts the merged Timeseries
func (se *SeriesEnvelope) Merge(sort bool, collection ...timeseries.Timeseries) {
	series := make(map[seriesKey]*models.Row)
	for i, r := range se.Results {
		for j, s := range se.Results[i].Series {
			series[seriesKey{StatementID: r.StatementID, Name: s.Name, Tags: tags(s.Tags).String(), Columns: strings.Join(s.Columns, ",")}] = &se.Results[i].Series[j]
		}
	}
	for _, ts := range collection {
		if ts != nil {
			se2 := ts.(*SeriesEnvelope)
			for _, r := range se2.Results {
				for _, s := range r.Series {
					sk := seriesKey{StatementID: r.StatementID, Name: s.Name, Tags: tags(s.Tags).String(), Columns: strings.Join(s.Columns, ",")}
					if o, ok := series[sk]; !ok {
						series[sk] = o
						continue
					}
					series[sk].Values = append(series[sk].Values, s.Values...)
				}
			}
			se.ExtentList = append(se.ExtentList, se2.ExtentList...)
		}
	}
	se.ExtentList = se.ExtentList.Compress(se.StepDuration)
	if sort {
		se.Sort()
	}
}

// Copy returns a perfect copy of the base Timeseries
func (se *SeriesEnvelope) Copy() timeseries.Timeseries {
	resultSe := &SeriesEnvelope{
		Err:          se.Err,
		Results:      make([]Result, len(se.Results)),
		StepDuration: se.StepDuration,
		ExtentList:   make(timeseries.ExtentList, len(se.ExtentList)),
	}
	copy(resultSe.ExtentList, se.ExtentList)
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

// Crop returns a copy of the base Timeseries that has been cropped down to the provided Extents.
// Crop assumes the base Timeseries is already sorted, and will corrupt an unsorted Timeseries
func (se *SeriesEnvelope) Crop(e timeseries.Extent) {

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
	if se.ExtentList.Contains(e) {
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
					if i < len(se.Results[i].Series) {
						se.Results[i].Series = append(se.Results[i].Series[:j], se.Results[i].Series[j+1:]...)
					} else {
						se.Results[i].Series = se.Results[i].Series[:len(se.Results[i].Series)-1]
					}
				}
			}
		}
	}
	se.ExtentList = se.ExtentList.Crop(e)
}

// Sort sorts all Values in each Series chronologically by their timestamp
func (se *SeriesEnvelope) Sort() {

	if len(se.Results) == 0 || len(se.Results[0].Series) == 0 {
		return
	}

	m := make(map[int64][]interface{})
	if ti := str.IndexOfString(se.Results[0].Series[0].Columns, "time"); ti != -1 {
		for ri := range se.Results {
			for si := range se.Results[ri].Series {
				for _, v := range se.Results[ri].Series[si].Values {
					m[int64(v[ti].(float64))] = v
				}

				keys := make([]int64, 0, len(m))
				for key := range m {
					keys = append(keys, key)
				}

				sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
				sm := make([][]interface{}, 0, len(keys))
				for _, key := range keys {
					sm = append(sm, m[key])
				}
				se.Results[ri].Series[si].Values = sm
			}
		}
	}
}
