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

package influxql

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
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
	err := json.NewDecoder(reader).Decode(wfd)
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
			if len(wfd.Results[i].SeriesList[j].Columns) < 2 {
				return nil, timeseries.ErrInvalidBody
			}
			var timeFound bool
			cl := len(wfd.Results[i].SeriesList[j].Columns)
			fdl := cl - 1 // -1 excludes time column from list for DataSet format
			sh.ValueFieldsList = make([]timeseries.FieldDefinition, fdl)
			var fdi int
			for ci, cn := range wfd.Results[i].SeriesList[j].Columns {
				if cn == "time" || cn == "_time" {
					timeFound = true
					sh.TimestampField = timeseries.FieldDefinition{Name: cn, OutputPosition: ci}
					continue
				}
				sh.ValueFieldsList[fdi] = timeseries.FieldDefinition{Name: cn}
				fdi++
			}
			if !timeFound || wfd.Results[i].SeriesList[j].Values == nil {
				return nil, timeseries.ErrInvalidBody
			}
			sh.CalculateSize()
			pts := make(dataset.Points, len(wfd.Results[i].SeriesList[j].Values))
			var sz int64
			var wg sync.WaitGroup
			errs := make([]error, len(wfd.Results[i].SeriesList[j].Values))
			for vi, v := range wfd.Results[i].SeriesList[j].Values {
				wg.Go(func() {
					pt, cols, err := pointFromValues(v, sh.TimestampField.OutputPosition)
					if err != nil {
						errs[vi] = err
						return
					}
					if pt.Epoch == 0 {
						return
					}
					if vi == 0 {
						for x := range cols {
							sh.ValueFieldsList[x].DataType = cols[x]
						}
					}
					pts[vi] = pt
					atomic.AddInt64(&sz, int64(pt.Size))
					wfd.Results[i].SeriesList[j].Values[vi] = nil
				})
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				return nil, err
			}
			sort.Sort(pts)
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

// tryParseTimestamp tries to parse a nanosecond timestamp from the value of a given field.
// This assumes that, if a field is in number format, it is in nanoseconds; otherwise it
// tried to parse some standard non-numeric formats. Returns -1 for invalid formats.
//
// tryParseTimestamp checks int ns, float ns and the following string formats:
//   - RFC3339
//   - RFC3339 (Nanoseconds)
func tryParseTimestamp(v any) int64 {
	if ns, ok := v.(int64); ok {
		return ns
	} else if fns, ok := v.(float64); ok {
		return int64(fns)
	} else if sns, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339, sns); err == nil {
			return t.UnixNano()
		} else if t, err := time.Parse(time.RFC3339Nano, sns); err == nil {
			return t.UnixNano()
		}
	}
	return -1
}

func pointFromValues(v []any, tsIndex int) (dataset.Point,
	[]timeseries.FieldDataType, error) {
	p := dataset.Point{}
	ns := tryParseTimestamp(v[tsIndex])
	if ns == -1 {
		return p, nil, timeseries.ErrInvalidTimeFormat
	}
	p.Values = append(make([]any, 0, len(v)-1), v[:tsIndex]...)
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
