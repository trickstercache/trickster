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

package model

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"sync"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/timeseries/dataset"
	"github.com/tricksterproxy/trickster/pkg/timeseries/epoch"
)

// Unmarshal performs a standard unmarshal of the bytes into the InfluxDB Wire Format Document,
// and then converts it into the Common Time Series Format

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	buf := bytes.NewReader(data)
	return UnmarshalTimeseriesReader(buf, trq)
}

// UnmarshalTimeseriesReader converts a JSON blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	wfd := &WFDocument{}
	d := json.NewDecoder(reader)
	err := d.Decode(wfd)
	if err != nil {
		return nil, err
	}
	ds := &dataset.DataSet{
		Error:          wfd.Err,
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}
	if wfd.Results == nil {
		return nil, timeseries.ErrInvalidBody
	}
	ds.Results = make([]*dataset.Result, len(wfd.Results))
	for i := range wfd.Results {
		ds.Results[i] = &dataset.Result{
			StatementID: wfd.Results[i].StatementID,
			Error:       wfd.Results[i].Err,
		}
		if wfd.Results[i].SeriesList == nil {
			continue
		}
		ds.Results[i].SeriesList = make([]*dataset.Series, len(wfd.Results[i].SeriesList))
		for j := range wfd.Results[i].SeriesList {
			sh := dataset.SeriesHeader{
				Name:           wfd.Results[i].SeriesList[j].Name,
				Tags:           dataset.Tags(wfd.Results[i].SeriesList[j].Tags),
				QueryStatement: trq.Statement,
			}
			if wfd.Results[i].SeriesList[j].Columns == nil ||
				len(wfd.Results[i].SeriesList[j].Columns) < 2 {
				return nil, timeseries.ErrInvalidBody
			}
			var timeFound bool
			cl := len(wfd.Results[i].SeriesList[j].Columns)
			fdl := cl - 1 // -1 excludes time column from list for DataSet format
			sh.FieldsList = make([]timeseries.FieldDefinition, fdl)
			var fdi int
			for ci, cn := range wfd.Results[i].SeriesList[j].Columns {
				if cn == "time" {
					timeFound = true
					sh.TimestampIndex = ci
					continue
				}
				sh.FieldsList[fdi] = timeseries.FieldDefinition{Name: cn}
				fdi++
			}
			if !timeFound || wfd.Results[i].SeriesList[j].Values == nil {
				return nil, timeseries.ErrInvalidBody
			}
			sh.CalculateSize()
			pts := make(dataset.Points, 0, len(wfd.Results[i].SeriesList[j].Values))
			var sz int64
			var mtx sync.Mutex
			var wg sync.WaitGroup
			var ume error
			for vi, v := range wfd.Results[i].SeriesList[j].Values {
				wg.Add(1)
				go func(vals []interface{}, idx int) {
					pt, cols, err := pointFromValues(vals, sh.TimestampIndex)
					if err != nil {
						ume = err
						wg.Done()
						return
					}
					if pt.Epoch == 0 {
						wg.Done()
						return
					}
					if idx == 0 {
						for x := range cols {
							sh.FieldsList[x].DataType = cols[x]
						}
					}
					mtx.Lock()
					pts = append(pts, pt)
					sz += int64(pt.Size)
					wfd.Results[i].SeriesList[j].Values[idx] = nil
					mtx.Unlock()
					wg.Done()
				}(v, vi)
				if ume != nil {
					break
				}
			}
			wg.Wait()
			sort.Sort(pts)
			if ume != nil {
				return nil, ume
			}
			s := &dataset.Series{
				Header:    sh,
				Points:    pts,
				PointSize: sz,
			}
			ds.Results[i].SeriesList[j] = s
			wfd.Results[i].SeriesList[j].Values = nil
		}
	}
	return ds, nil
}

func pointFromValues(v []interface{}, tsIndex int) (dataset.Point,
	[]timeseries.FieldDataType, error) {
	p := dataset.Point{}
	ns, ok := v[tsIndex].(int64)
	if !ok {
		fns, ok := v[tsIndex].(float64)
		if !ok {
			return p, nil, timeseries.ErrInvalidTimeFormat
		}
		ns = int64(fns)
	}
	p.Values = append(make([]interface{}, 0, len(v)-1), v[:tsIndex]...)
	p.Values = append(p.Values, v[tsIndex+1:]...)
	p.Epoch = epoch.Epoch(ns)
	p.Size = 12
	fdts := make([]timeseries.FieldDataType, len(p.Values))
	for x := range p.Values {
		if p.Values[x] == nil {
			continue
		}
		switch t := p.Values[x].(type) {
		case string:
			fdts[x] = timeseries.String
			p.Size += len(t)
		case bool:
			fdts[x] = timeseries.Bool
			p.Size++
		case int64, int:
			fdts[x] = timeseries.Int64
			p.Size += 8
		case float64, float32:
			fdts[x] = timeseries.Float64
			p.Size += 8
		default:
			return p, nil, timeseries.ErrInvalidTimeFormat
		}
	}
	return p, fdts, nil
}
