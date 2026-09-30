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
	"io"
	"net/http"
	"strconv"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"

	"github.com/influxdata/influxdb/models"
)

const timeColumnName = "time"

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int,
) ([]byte, error) {
	ds, err := dataSetOf(ts)
	if err != nil {
		return nil, err
	}
	switch {
	case rlo == nil || rlo.OutputFormat == 0:
		var buf bytes.Buffer
		if err := writeDocument(&buf, ds, rlo); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case rlo.OutputFormat == 1:
		wfdoc, err := toWireFormat(ds.Flat(), rlo)
		if err != nil {
			return nil, err
		}
		return json.MarshalIndent(wfdoc, "", "  ")
	default:
		return nil, timeseries.ErrUnknownFormat
	}
}

// MarshalTimeseriesWriter writes a Timeseries as a JSON blob to an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int, w io.Writer,
) error {
	ds, err := dataSetOf(ts)
	if err != nil {
		return err
	}
	if rw, ok := w.(http.ResponseWriter); ok && rw != nil {
		rw.Header().Add(headers.NameContentType, headers.ValueApplicationJSON)
	}
	if rlo != nil && rlo.OutputFormat == 1 {
		wfdoc, err := toWireFormat(ds.Flat(), rlo)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(wfdoc)
	}
	return writeDocument(w, ds, rlo, '\n')
}

func dataSetOf(ts timeseries.Timeseries) (*dataset.DataSet, error) {
	if ts == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	return ds, nil
}

// writes ds to w as encoding/json writes its wire format document, followed by suffix; nothing is
// written when a value can't be, as JSON has no NaN or infinities
func writeDocument(w io.Writer, ds *dataset.DataSet, rlo *timeseries.RequestOptions, suffix ...byte) error {
	if err := checkValues(ds); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(w)
	appendDocument(&cw, ds, newTimeFormat(rlo))
	cw.Buf = append(cw.Buf, suffix...)
	return cw.Close()
}

func checkValues(ds *dataset.DataSet) error {
	if ds == nil {
		return nil
	}
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			segs := s.Segments()
			for k := range segs {
				seg := &segs[k]
				for c := range seg.NumCols() {
					if err := seg.CheckColumnJSON(c); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// how a point's time is written: an RFC 3339 string, or an integer count of divisor nanoseconds
type timeFormat struct {
	rfc3339 bool
	divisor int64
}

func newTimeFormat(rlo *timeseries.RequestOptions) timeFormat {
	if rlo == nil || rlo.TimeFormat == 0 {
		return timeFormat{rfc3339: true}
	}
	if m, ok := epochMultipliers[rlo.TimeFormat]; ok {
		return timeFormat{divisor: m}
	}
	return timeFormat{divisor: 1}
}

func (f timeFormat) append(b []byte, e epoch.Epoch) []byte {
	if f.rfc3339 {
		b = append(b, '"')
		b = time.Unix(0, int64(e)).UTC().AppendFormat(b, time.RFC3339Nano)
		return append(b, '"')
	}
	return strconv.AppendInt(b, int64(e)/f.divisor, 10)
}

func appendDocument(cw *tbytes.ChunkWriter, ds *dataset.DataSet, tf timeFormat) {
	if ds == nil {
		cw.Buf = append(cw.Buf, "null"...)
		return
	}
	if len(ds.Results) == 0 {
		cw.Buf = append(cw.Buf, `{"results":null}`...)
		return
	}
	cw.Buf = append(cw.Buf, `{"results":[`...)
	wrote := false
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		if wrote {
			cw.Buf = append(cw.Buf, ',')
		}
		wrote = true
		cw.Buf = append(cw.Buf, `{"statement_id":`...)
		cw.Buf = strconv.AppendInt(cw.Buf, int64(r.StatementID), 10)
		series := false
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			if series {
				cw.Buf = append(cw.Buf, ',')
			} else {
				cw.Buf = append(cw.Buf, `,"series":[`...)
				series = true
			}
			appendSeries(cw, s, tf)
		}
		if series {
			cw.Buf = append(cw.Buf, ']')
		}
		cw.Buf = append(cw.Buf, '}')
	}
	cw.Buf = append(cw.Buf, "]}"...)
}

// appends s as a models.Row, whose name, tags and values are left out when empty
// the most columns of a series whose floats are read without their kinds
const maxFloatColumns = 8

func appendSeries(cw *tbytes.ChunkWriter, s *dataset.Series, tf timeFormat) {
	h := &s.Header
	at := h.TimestampField.OutputPosition
	cw.Buf = append(cw.Buf, '{')
	b := cw.Buf
	if h.Name != "" {
		b = append(b, `"name":`...)
		b = tstrings.AppendJSON(b, h.Name)
		b = append(b, ',')
	}
	if len(h.Tags) > 0 {
		b = append(b, `"tags":`...)
		b = h.Tags.AppendJSON(b)
		b = append(b, ',')
	}
	b = append(b, `"columns":[`...)
	timed := false
	for i, fd := range h.ValueFieldsList {
		if i == at {
			b = append(b, `"`+timeColumnName+`",`...)
			timed = true
		}
		b = tstrings.AppendJSON(b, fd.Name)
		b = append(b, ',')
	}
	if !timed {
		b = append(b, `"`+timeColumnName+`",`...)
	}
	b[len(b)-1] = ']'
	values := false
	segs := s.Segments()
	// each all-float column is read as a []float64, skipping the kind switch for each of its values
	var floatBuf [maxFloatColumns][]float64
	for k := range segs {
		seg := &segs[k]
		cols := seg.NumCols()
		if cols == 0 {
			continue
		}
		floats := floatBuf[:0]
		for n := range min(cols, maxFloatColumns) {
			col := seg.Col(n)
			fs, _ := col.Float64s()
			floats = append(floats, fs)
		}
		for i, e := range seg.Epochs() {
			if values {
				b = append(b, ",["...)
			} else {
				b = append(b, `,"values":[[`...)
				values = true
			}
			timed := false
			for n := range cols {
				if n == at {
					b = append(tf.append(b, e), ',')
					timed = true
				}
				// the values were checked, so none fails
				if n < len(floats) && floats[n] != nil {
					b, _ = tstrings.AppendJSONFloat(b, floats[n][i], 64)
				} else {
					b, _ = seg.AppendJSON(b, n, i)
				}
				b = append(b, ',')
			}
			if !timed {
				b = append(tf.append(b, e), ',')
			}
			b[len(b)-1] = ']'
			cw.Buf = b
			cw.FlushIfFull()
			b = cw.Buf
		}
	}
	if values {
		b = append(b, ']')
	}
	b = append(b, '}')
	cw.Buf = b
}

func formatRFC3339Time(epoch epoch.Epoch, _ int64) any {
	t := time.Unix(0, int64(epoch))
	return t.UTC().Format(time.RFC3339Nano)
}

func formatEpochTime(epoch epoch.Epoch, m int64) any {
	return int64(epoch) / m
}

func toWireFormat(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions,
) (*WFDocument, error) {
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

			row.Values = make([][]any, 0, s.PointCount())

			for _, p := range s.Points() {
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
