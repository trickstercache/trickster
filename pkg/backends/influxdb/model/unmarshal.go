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

package model

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/influxdata/influxdb/models"
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

func decodeJSON(reader io.Reader) (*WFDocument, error) {
	wfd := &WFDocument{}
	err := json.NewDecoder(reader).Decode(wfd)
	if err != nil {
		return nil, err
	}
	return wfd, nil
}

func decodeCSV(reader io.Reader) (*WFDocument, error) {
	b, _ := io.ReadAll(reader)
	reader = bytes.NewReader(b)
	records, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return nil, err
	}
	var columns []string
	var rows int = len(records) - 1
	if len(records) == 0 {
		rows = 0
	}
	wfd := &WFDocument{
		Results: []WFResult{
			{StatementID: 0, SeriesList: make([]models.Row, rows)},
		},
	}
	for ri, r := range records {
		// Do headers at first row
		if ri == 0 {
			columns = r
			continue
		}
		// Construct WFD row from record
		row := models.Row{
			// Name, Tags deliberately left empty, they don't show up here
			Columns: columns,
			Values:  [][]interface{}{make([]interface{}, len(r))},
		}
		for ii, item := range r {
			var val any
			if f, err := strconv.ParseFloat(item, 64); err == nil {
				val = f
			} else if x, err := strconv.ParseInt(item, 10, 64); err == nil {
				val = x
			} else if b, err := strconv.ParseBool(item); err == nil {
				val = b
			} else {
				val = item
			}
			row.Values[0][ii] = val
		}
		wfd.Results[0].SeriesList[ri-1] = row
	}
	return wfd, nil
}

// UnmarshalTimeseriesReader converts a JSON blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	var bck bytes.Buffer
	tr := io.TeeReader(reader, &bck)
	wfd, err := decodeCSV(tr)
	if err != nil {
		wfd, err = decodeJSON(&bck)
		if err != nil {
			return nil, err
		}
	}
	//wfd := &WFDocument{}
	//d := json.NewDecoder(reader)
	//err := d.Decode(wfd)
	/*
		if err != nil {
			return nil, err
		}
	*/
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
				if cn == "time" || cn == "_time" {
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

func pointFromValues(v []interface{}, tsIndex int) (dataset.Point,
	[]timeseries.FieldDataType, error) {
	p := dataset.Point{}
	ns := tryParseTimestamp(v[tsIndex])
	if ns == -1 {
		return p, nil, timeseries.ErrInvalidTimeFormat
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
