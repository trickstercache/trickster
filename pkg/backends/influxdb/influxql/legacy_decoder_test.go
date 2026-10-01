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
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/influxdata/influxdb/models"
	"golang.org/x/sync/errgroup"
)

// legacyWFDocument is the wire format document both legacy paths used
type legacyWFDocument struct {
	Results []*legacyWFResult `json:"results"`
	Err     string            `json:"error,omitempty"`
}

type legacyWFResult struct {
	StatementID int           `json:"statement_id"`
	SeriesList  []*models.Row `json:"series,omitempty"`
	Err         string        `json:"error,omitempty"`
}

// legacyUnmarshalTimeseriesReader is the decoder the stream decoder replaced, kept as its oracle
func legacyUnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	wfd := &legacyWFDocument{}
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
			var eg errgroup.Group
			eg.SetLimit(runtime.GOMAXPROCS(0))
			errs := make([]error, len(wfd.Results[i].SeriesList[j].Values))
			for vi, v := range wfd.Results[i].SeriesList[j].Values {
				eg.Go(func() error {
					pt, cols, err := legacyPointFromValues(v, sh.TimestampField.OutputPosition)
					if err != nil {
						errs[vi] = err
						return err
					}
					if pt.Epoch == 0 {
						return nil
					}
					if vi == 0 {
						for x := range cols {
							sh.ValueFieldsList[x].DataType = cols[x]
						}
					}
					pts[vi] = pt
					wfd.Results[i].SeriesList[j].Values[vi] = nil
					return nil
				})
			}
			eg.Wait()
			if err := errors.Join(errs...); err != nil {
				return nil, err
			}
			// a row at epoch zero leaves an empty point, which no series holds
			pts = slices.DeleteFunc(pts, func(p dataset.Point) bool { return p.Values == nil })
			slices.SortFunc(pts, func(a, b dataset.Point) int {
				if a.Epoch < b.Epoch {
					return -1
				}
				if a.Epoch > b.Epoch {
					return 1
				}
				return 0
			})
			ds.Results[i].SeriesList[j] = dataset.NewSeries(sh, pts)
			wfd.Results[i].SeriesList[j].Values = nil
		}
	}
	return ds, nil
}

// legacyTryParseTimestamp tries to parse a nanosecond timestamp from the value of a given field.
// This assumes that, if a field is in number format, it is in nanoseconds; otherwise it
// tried to parse some standard non-numeric formats. Returns -1 for invalid formats.
//
// legacyTryParseTimestamp checks int ns, float ns and the following string formats:
//   - RFC3339
//   - RFC3339 (Nanoseconds)
func legacyTryParseTimestamp(v any) int64 {
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

func legacyPointFromValues(v []any, tsIndex int) (dataset.Point,
	[]timeseries.FieldDataType, error,
) {
	p := dataset.Point{}
	ns := legacyTryParseTimestamp(v[tsIndex])
	if ns == -1 {
		return p, nil, timeseries.ErrInvalidTimeFormat
	}
	p.Values = append(make([]any, 0, len(v)-1), v[:tsIndex]...)
	p.Values = append(p.Values, v[tsIndex+1:]...)
	p.Epoch = epoch.Epoch(ns)
	fdts := make([]timeseries.FieldDataType, len(p.Values))
	for x := range p.Values {
		if p.Values[x] == nil {
			continue
		}
		switch p.Values[x].(type) {
		case string:
			fdts[x] = timeseries.String
		case bool:
			fdts[x] = timeseries.Bool
		case int64, int:
			fdts[x] = timeseries.Int64
		case float64, float32:
			fdts[x] = timeseries.Float64
		default:
			return p, nil, timeseries.ErrInvalidTimeFormat
		}
	}
	return p, fdts, nil
}

func legacyFormatRFC3339Time(epoch epoch.Epoch, _ int64) any {
	t := time.Unix(0, int64(epoch))
	return t.UTC().Format(time.RFC3339Nano)
}

func legacyFormatEpochTime(epoch epoch.Epoch, m int64) any {
	return int64(epoch) / m
}

// legacyToWireFormat built the document the pretty path marshaled with encoding/json
func legacyToWireFormat(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions,
) (*legacyWFDocument, error) {
	if ds == nil {
		return nil, nil
	}
	df, multiplier := legacyDateFormatter(rlo)
	out := &legacyWFDocument{}
	lr := len(ds.Results)
	if lr > 0 {
		out.Results = make([]*legacyWFResult, 0, lr)
	}
	for _, dr := range ds.Results {
		res := &legacyWFResult{
			StatementID: dr.StatementID,
		}
		ls := len(dr.SeriesList)
		if ls > 0 {
			res.SeriesList = make([]*models.Row, 0, ls)
		}
		for _, s := range dr.SeriesList {
			if s == nil {
				continue
			}
			row := &models.Row{
				Name: s.Header.Name,
				Tags: s.Header.Tags,
			}
			row.Columns = make([]string, 0, len(s.Header.ValueFieldsList)+1)
			var tsColumnAdded bool
			for i, header := range s.Header.ValueFieldsList {
				if i == s.Header.TimestampField.OutputPosition {
					row.Columns = append(row.Columns, timeColumnName)
					tsColumnAdded = true
				}
				row.Columns = append(row.Columns, header.Name)
			}
			if !tsColumnAdded {
				row.Columns = append(row.Columns, timeColumnName)
				tsColumnAdded = true
			}

			row.Values = make([][]any, 0, s.PointCount())

			for _, p := range dspoints.Of(s) {
				if len(p.Values) == 0 {
					continue
				}
				vals := make([]any, 0, len(p.Values))
				var tsValAdded bool
				for n, v := range p.Values {
					if n == s.Header.TimestampField.OutputPosition {
						vals = append(vals, df(p.Epoch, multiplier))
						tsValAdded = true
					}
					vals = append(vals, v)
				}
				if !tsValAdded {
					vals = append(vals, df(p.Epoch, multiplier))
				}
				row.Values = append(row.Values, vals)
			}
			res.SeriesList = append(res.SeriesList, row)
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

type legacyDateFormatterFunc func(epoch.Epoch, int64) any

func legacyDateFormatter(rlo *timeseries.RequestOptions) (legacyDateFormatterFunc, int64) {
	var df legacyDateFormatterFunc
	var tf byte
	var multiplier int64

	if rlo != nil {
		tf = rlo.TimeFormat
	}
	switch tf {
	case 0:
		df = legacyFormatRFC3339Time
	default:
		if m, ok := epochMultipliers[tf]; ok {
			multiplier = m
		} else {
			multiplier = 1
		}
		df = legacyFormatEpochTime
	}
	return df, multiplier
}
