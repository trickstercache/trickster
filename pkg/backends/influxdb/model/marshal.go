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
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/influxdb/models"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const timeColumnName = "time"

var marshalers = map[byte]dataset.Marshaler{
	0: marshalTimeseriesJSONMinified,
	1: marshalTimeseriesJSONPretty,
	2: marshalTimeseriesCSV,
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	if ts == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	marshaler, ok := marshalers[of]
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	return marshaler(ds, rlo, status)
}

// MarshalTimeseriesWriter writes a Timeseries as a JSON blob to an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	b, err := MarshalTimeseries(ts, rlo, status)
	if err != nil {
		return err
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	err = response.WriteResponseHeader(w, status, of, nil)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func formatRFC3339Time(e epoch.Epoch, m int64) any {
	t := time.Unix(0, int64(e))
	return t.UTC().Format(time.RFC3339Nano)
}

func formatEpochTime(e epoch.Epoch, m int64) any {
	return int64(e) / m
}

func writeRFC3339Time(w io.Writer, e epoch.Epoch, m int64) {
	t := time.Unix(0, int64(e))
	w.Write([]byte(`"` + t.Format(time.RFC3339Nano) + `"`))
}

func writeEpochTime(w io.Writer, e epoch.Epoch, m int64) {
	w.Write([]byte(strconv.FormatInt(int64(e)/m, 10)))
}

func writeCSVValue(w io.Writer, v any, nilVal string) {
	if v == nil {
		w.Write([]byte(nilVal))
	}
	switch t := v.(type) {
	case string:
		if strings.Contains(t, `"`) {
			t = `"` + strings.ReplaceAll(t, `"`, `\"`) + `"`
		} else if strings.Contains(t, " ") || strings.Contains(t, ",") {
			t = `"` + t + `"`
		}
		w.Write([]byte(t))
	case bool:
		w.Write([]byte(strconv.FormatBool(t)))
	case int64:
		w.Write([]byte(strconv.FormatInt(t, 10)))
	case int:
		w.Write([]byte(strconv.Itoa(t)))
	case float64:
		w.Write([]byte(strconv.FormatFloat(t, 'f', -1, 64)))
	}
}

func marshalTimeseriesJSONMinified(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	return marshalTimeseriesJSON(ds, rlo, status, false)
}

func marshalTimeseriesJSONPretty(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	return marshalTimeseriesJSON(ds, rlo, status, true)
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
			row.Columns = make([]string, 0, len(s.Header.FieldsList)+1)
			var tsColumnAdded bool
			for i, header := range s.Header.FieldsList {
				if i == s.Header.TimestampIndex {
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
					if n == s.Header.TimestampIndex {
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

func marshalTimeseriesJSON(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	_ int, pretty bool) ([]byte, error) {
	if ds == nil {
		return nil, nil
	}
	wfdoc, err := toWireFormat(ds, rlo)
	if err != nil {
		return nil, err
	}
	var b []byte
	if pretty {
		b, err = json.MarshalIndent(wfdoc, "", "  ")
	} else {
		b, err = json.Marshal(wfdoc)
	}
	return b, err
}

func marshalTimeseriesCSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	status int) ([]byte, error) {
	var headerWritten bool
	dw, multiplier := getDateWriter(rlo)
	if ds == nil {
		return nil, nil
	}
	w := new(bytes.Buffer)
	for _, s := range ds.Results[0].SeriesList {
		if s == nil {
			continue
		}
		if !headerWritten {
			l := len(s.Header.FieldsList) + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = timeColumnName
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte("name,tags," + strings.Join(cols, ",") + "\n"))
			headerWritten = true
		}
		for _, p := range s.Points {
			w.Write([]byte(s.Header.Name + ",\"" + s.Header.Tags.StringsWithSep("=", ",") + "\","))
			lv := len(p.Values)
			for n, v := range p.Values {
				if n == s.Header.TimestampIndex {
					dw(w, p.Epoch, multiplier)
					if n < lv-1 {
						w.Write([]byte(","))
					}
					n++
				}
				writeCSVValue(w, v, "")
				if n < lv-1 {
					w.Write([]byte{','})
				}
			}
			w.Write([]byte("\n"))
		}
	}
	return w.Bytes(), nil
}

type dateFormatter func(epoch.Epoch, int64) any
type dateWriter func(io.Writer, epoch.Epoch, int64)

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

func getDateWriter(rlo *timeseries.RequestOptions) (dateWriter, int64) {
	var dw dateWriter
	var tf byte
	var multiplier int64
	if rlo != nil {
		tf = rlo.TimeFormat
	}
	switch tf {
	case 0:
		dw = writeRFC3339Time
	default:
		if m, ok := epochMultipliers[tf]; ok {
			multiplier = m
		} else {
			multiplier = 1
		}
		dw = writeEpochTime
	}
	return dw, multiplier
}
