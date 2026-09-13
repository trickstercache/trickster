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

package sql

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// MarshalTimeseries converts a Timeseries into a v3 response body
func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int,
) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := MarshalTimeseriesWriter(ts, rlo, 0, buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MarshalTimeseriesWriter converts a Timeseries into a v3 response body via io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int, w io.Writer,
) error {
	if ts == nil {
		return timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	if hw, ok := w.(http.ResponseWriter); ok {
		switch of {
		case iofmt.V3OutputCSV:
			hw.Header().Set(headers.NameContentType, headers.ValueApplicationCSV)
		default:
			hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		}
	}
	switch of {
	case iofmt.V3OutputJSONL:
		return marshalJSONL(w, ds)
	case iofmt.V3OutputCSV:
		return marshalCSV(w, ds)
	default:
		return marshalJSON(w, ds)
	}
}

// v3TimestampOutputLayout matches InfluxDB 3's native output shape: naive UTC
// with no zone suffix, fractional seconds only when present.
const v3TimestampOutputLayout = "2006-01-02T15:04:05.999999999"

// v3Row is one output row with column order preserved.
type v3Row struct {
	columns []string
	values  []any
	epoch   epoch.Epoch
}

// dataSetRows flattens a DataSet into output rows: the timestamp column,
// then the series' tag columns, then value columns, in header order.
func dataSetRows(ds *dataset.DataSet) []v3Row {
	var rows []v3Row
	for _, result := range ds.Results {
		for _, series := range result.SeriesList {
			tsName := series.Header.TimestampField.Name
			if tsName == "" {
				tsName = DefaultTimestampField
			}
			columns := make([]string, 0,
				1+len(series.Header.TagFieldsList)+len(series.Header.ValueFieldsList))
			columns = append(columns, tsName)
			for _, fd := range series.Header.TagFieldsList {
				columns = append(columns, fd.Name)
			}
			for _, fd := range series.Header.ValueFieldsList {
				columns = append(columns, fd.Name)
			}
			for _, pt := range series.Points {
				values := make([]any, 0, len(columns))
				values = append(values,
					time.Unix(0, int64(pt.Epoch)).UTC().Format(v3TimestampOutputLayout))
				for _, fd := range series.Header.TagFieldsList {
					values = append(values, series.Header.Tags[fd.Name])
				}
				for i := range series.Header.ValueFieldsList {
					if i < len(pt.Values) {
						values = append(values, pt.Values[i])
					} else {
						values = append(values, nil)
					}
				}
				rows = append(rows, v3Row{columns: columns, values: values, epoch: pt.Epoch})
			}
		}
	}
	if ds.TimeRangeQuery != nil {
		sortV3Rows(rows, ds.TimeRangeQuery.Ordering)
	}
	return rows
}

func sortV3Rows(rows []v3Row, ordering []timeseries.OrderTerm) {
	if len(rows) < 2 || len(ordering) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b v3Row) int {
		for _, term := range ordering {
			comparison := compareV3Row(a, b, term)
			if comparison == 0 {
				continue
			}
			return comparison
		}
		return 0
	})
}

func compareV3Row(a, b v3Row, term timeseries.OrderTerm) int {
	av, aTimestamp, aFound := v3RowValue(a, term.Column)
	bv, bTimestamp, bFound := v3RowValue(b, term.Column)
	if !aFound || !bFound {
		return 0
	}
	if av == nil || bv == nil {
		switch {
		case av == nil && bv == nil:
			return 0
		case av == nil && term.NullsFirst:
			return -1
		case av == nil:
			return 1
		case term.NullsFirst:
			return 1
		default:
			return -1
		}
	}
	if aTimestamp && bTimestamp {
		return applyV3Direction(cmp.Compare(a.epoch, b.epoch), term.Descending)
	}
	return applyV3Direction(compareV3Value(av, bv), term.Descending)
}

func applyV3Direction(comparison int, descending bool) int {
	if descending {
		return -comparison
	}
	return comparison
}

func v3RowValue(row v3Row, column string) (any, bool, bool) {
	for i, name := range row.columns {
		if name == column {
			return row.values[i], i == 0, true
		}
	}
	return nil, false, false
}

func compareV3Value(a, b any) int {
	switch av := a.(type) {
	case int64:
		if bv, ok := b.(int64); ok {
			return cmp.Compare(av, bv)
		}
	case uint64:
		if bv, ok := b.(uint64); ok {
			return cmp.Compare(av, bv)
		}
	case float64:
		if bv, ok := b.(float64); ok {
			if math.IsNaN(av) {
				if math.IsNaN(bv) {
					return 0
				}
				return 1
			}
			if math.IsNaN(bv) {
				return -1
			}
			return cmp.Compare(av, bv)
		}
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv)
		}
	case bool:
		if bv, ok := b.(bool); ok {
			switch {
			case av == bv:
				return 0
			case !av:
				return -1
			default:
				return 1
			}
		}
	}
	return cmp.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func marshalJSON(w io.Writer, ds *dataset.DataSet) error {
	rows := dataSetRows(ds)
	buf := bufWriter(w)
	if _, err := buf.WriteString("["); err != nil {
		return err
	}
	for i, row := range rows {
		if i > 0 {
			if _, err := buf.WriteString(","); err != nil {
				return err
			}
		}
		if err := writeOrderedObject(buf, row); err != nil {
			return err
		}
	}
	if _, err := buf.WriteString("]\n"); err != nil {
		return err
	}
	return buf.Flush()
}

func marshalJSONL(w io.Writer, ds *dataset.DataSet) error {
	rows := dataSetRows(ds)
	buf := bufWriter(w)
	for _, row := range rows {
		if err := writeOrderedObject(buf, row); err != nil {
			return err
		}
		if _, err := buf.WriteString("\n"); err != nil {
			return err
		}
	}
	return buf.Flush()
}

func marshalCSV(w io.Writer, ds *dataset.DataSet) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	var lastColumns []string
	for _, row := range dataSetRows(ds) {
		// one header row per column layout; series sharing a layout share it
		if !equalColumns(lastColumns, row.columns) {
			if err := cw.Write(row.columns); err != nil {
				return err
			}
			lastColumns = row.columns
		}
		record := make([]string, len(row.values))
		for i, v := range row.values {
			record[i] = formatValue(v)
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	return nil
}

func equalColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// flushWriter is the buffered-write surface shared by the JSON marshalers.
type flushWriter interface {
	io.Writer
	WriteString(string) (int, error)
	Flush() error
}

type nopFlushWriter struct{ *bytes.Buffer }

func (nopFlushWriter) Flush() error { return nil }

func bufWriter(w io.Writer) flushWriter {
	if b, ok := w.(*bytes.Buffer); ok {
		return nopFlushWriter{b}
	}
	return bufio.NewWriter(w)
}

// writeOrderedObject emits one row as a JSON object preserving column order,
// which encoding/json's map marshaling would alphabetize away.
func writeOrderedObject(w flushWriter, row v3Row) error {
	if _, err := w.WriteString("{"); err != nil {
		return err
	}
	for i, name := range row.columns {
		if i > 0 {
			if _, err := w.WriteString(","); err != nil {
				return err
			}
		}
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(key); err != nil {
			return err
		}
		if _, err := w.WriteString(":"); err != nil {
			return err
		}
		value, err := json.Marshal(row.values[i])
		if err != nil {
			return err
		}
		if _, err := w.Write(value); err != nil {
			return err
		}
	}
	_, err := w.WriteString("}")
	return err
}

func formatValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}
