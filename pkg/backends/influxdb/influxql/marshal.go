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
	"io"
	"net/http"
	"time"

	"github.com/influxdata/influxdb/models"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const timeColumnName = "time"

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int) ([]byte, error) {
	if ts == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	wfdoc, err := toWireFormat(ds, rlo)
	if err != nil {
		return nil, err
	}
	var b []byte
	switch {
	case rlo == nil || rlo.OutputFormat == 0:
		b, err = json.Marshal(wfdoc)
	case rlo.OutputFormat == 1:
		b, err = json.MarshalIndent(wfdoc, "", "  ")
	default:
		err = timeseries.ErrUnknownFormat
	}
	return b, err
}

// MarshalTimeseriesWriter writes a Timeseries as a JSON blob to an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int, w io.Writer) error {
	if ts == nil {
		return timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	wfdoc, err := toWireFormat(ds, rlo)
	if err != nil {
		return err
	}
	if rw, ok := w.(http.ResponseWriter); ok && rw != nil {
		rw.Header().Add(headers.NameContentType, headers.ValueApplicationJSON)
	}
	enc := json.NewEncoder(w)
	if rlo != nil && rlo.OutputFormat == 1 {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(wfdoc)
}

func formatRFC3339Time(epoch epoch.Epoch, _ int64) any {
	t := time.Unix(0, int64(epoch))
	return t.UTC().Format(time.RFC3339Nano)
}

func formatEpochTime(epoch epoch.Epoch, m int64) any {
	return int64(epoch) / m
}

func toWireFormat(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions) (*WFDocument, error) {
	if ds == nil {
		return nil, nil
	}
	df, multiplier := getDateFormatter(rlo)
	out := &WFDocument{}
	lr := len(ds.Results)
	if lr > 0 {
		out.Results = make([]*WFResult, 0, lr)
	}
	for _, dr := range ds.Results {
		res := &WFResult{
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

			row.Values = make([][]any, 0, len(s.Points))

			for _, p := range s.Points {
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

type dateFormatter func(epoch.Epoch, int64) any

func getDateFormatter(rlo *timeseries.RequestOptions) (dateFormatter, int64) {
	var df dateFormatter
	var tf byte
	var multiplier int64

	if rlo != nil {
		tf = rlo.TimeFormat
	}
	switch tf {
	case 0:
		df = formatRFC3339Time
	default:
		if m, ok := epochMultipliers[tf]; ok {
			multiplier = m
		} else {
			multiplier = 1
		}
		df = formatEpochTime
	}
	return df, multiplier
}
