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
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type sqlOutputColumn struct {
	name  string
	role  byte
	index int
	pos   int
}

const (
	sqlColumnTimestamp byte = iota
	sqlColumnTag
	sqlColumnValue
)

type sqlOutputRow struct {
	columns []sqlOutputColumn
	values  []any
	epoch   epoch.Epoch
}

func marshalSQLTimeseriesWriter(ds *dataset.DataSet, marker *SQLQueryPlan,
	writer io.Writer,
) error {
	if marker == nil || marker.Plan == nil {
		return timeseries.ErrUnknownFormat
	}
	if hw, ok := writer.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	rows := sqlOutputRows(ds)
	if ds != nil && ds.TimeRangeQuery != nil {
		sortSQLRows(rows, ds.TimeRangeQuery.Ordering)
	}
	if marker.ResponseFormat() == SQLResponseArray {
		return marshalSQLArrayRows(rows, marker, writer)
	}
	var w sqlFlushWriter
	if b, ok := writer.(*bytes.Buffer); ok {
		w = sqlNopFlushWriter{b}
	} else {
		w = bufio.NewWriter(writer)
	}
	if _, err := w.WriteString("["); err != nil {
		return err
	}
	for i, row := range rows {
		if i > 0 {
			if _, err := w.WriteString(","); err != nil {
				return err
			}
		}
		if err := writeSQLRow(w, row); err != nil {
			return err
		}
	}
	if _, err := w.WriteString("]\n"); err != nil {
		return err
	}
	return w.Flush()
}

func marshalSQLArrayRows(rows []sqlOutputRow, marker *SQLQueryPlan, writer io.Writer) error {
	if marker == nil || !marker.Header() {
		return timeseries.ErrUnknownFormat
	}
	columns := sqlOutputColumns(rows, marker)
	if hw, ok := writer.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	// Use an encoder for the complete rows so numbers retain their concrete
	// JSON representation and the output cannot accidentally emit malformed
	// separators when a writer returns a short write.
	output := make([][]any, 0, len(rows)+1)
	header := make([]any, len(columns))
	for i, column := range columns {
		header[i] = column.name
	}
	output = append(output, header)
	for _, row := range rows {
		values := make([]any, len(columns))
		for i, column := range columns {
			values[i] = sqlOutputColumnValue(row, column)
		}
		output = append(output, values)
	}
	return json.NewEncoder(writer).Encode(output)
}

func sqlOutputColumns(rows []sqlOutputRow, marker *SQLQueryPlan) []sqlOutputColumn {
	if len(rows) > 0 {
		columns := slices.Clone(rows[0].columns)
		slices.SortStableFunc(columns, func(a, b sqlOutputColumn) int {
			return cmp.Compare(a.pos, b.pos)
		})
		return columns
	}
	if marker == nil || marker.Plan == nil {
		return nil
	}
	if outputNames := marker.OutputColumns(); len(outputNames) > 0 {
		columns := make([]sqlOutputColumn, 0, len(outputNames))
		for pos, name := range outputNames {
			column := sqlOutputColumn{name: name, pos: pos}
			switch {
			case strings.EqualFold(name, marker.Plan.OutputColumn):
				column.role = sqlColumnTimestamp
			case sqlColumnNameIndex(marker.Plan.GroupColumns, name) >= 0:
				column.role = sqlColumnTag
				column.index = sqlColumnNameIndex(marker.Plan.GroupColumns, name)
			default:
				column.role = sqlColumnValue
				column.index = sqlColumnNameIndex(marker.Plan.ValueColumns, name)
			}
			columns = append(columns, column)
		}
		return columns
	}
	columns := make([]sqlOutputColumn, 0, 1+len(marker.Plan.GroupColumns)+len(marker.Plan.ValueColumns))
	columns = append(columns, sqlOutputColumn{name: marker.Plan.OutputColumn, role: sqlColumnTimestamp, pos: 0})
	for i, name := range marker.Plan.GroupColumns {
		columns = append(columns, sqlOutputColumn{name: name, role: sqlColumnTag, index: i, pos: i + 1})
	}
	for i, name := range marker.Plan.ValueColumns {
		columns = append(columns, sqlOutputColumn{name: name, role: sqlColumnValue, index: i, pos: i + 1 + len(marker.Plan.GroupColumns)})
	}
	return columns
}

func sqlColumnNameIndex(columns []string, name string) int {
	for i, column := range columns {
		if strings.EqualFold(column, name) {
			return i
		}
	}
	return -1
}

func sqlOutputColumnValue(row sqlOutputRow, column sqlOutputColumn) any {
	for i, source := range row.columns {
		if source.name == column.name && i < len(row.values) {
			return row.values[i]
		}
	}
	return nil
}

func sqlOutputRows(ds *dataset.DataSet) []sqlOutputRow {
	if ds == nil {
		return nil
	}
	var rows []sqlOutputRow
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			columns := make([]sqlOutputColumn, 0,
				1+len(series.Header.TagFieldsList)+len(series.Header.ValueFieldsList))
			timestamp := series.Header.TimestampField
			if timestamp.Name == "" {
				timestamp.Name = "__time"
			}
			columns = append(columns, sqlOutputColumn{
				name: timestamp.Name, role: sqlColumnTimestamp, pos: timestamp.OutputPosition,
			})
			for i, field := range series.Header.TagFieldsList {
				columns = append(columns, sqlOutputColumn{
					name: field.Name, role: sqlColumnTag, index: i, pos: field.OutputPosition,
				})
			}
			for i, field := range series.Header.ValueFieldsList {
				columns = append(columns, sqlOutputColumn{
					name: field.Name, role: sqlColumnValue, index: i, pos: field.OutputPosition,
				})
			}
			// Synthetic DataSets often leave OutputPosition at zero. Preserve
			// the conventional SQL order in that case (timestamp, tags, values).
			if !validSQLColumnPositions(columns) {
				for i := range columns {
					columns[i].pos = i
				}
			}
			for _, point := range series.Points {
				values := make([]any, len(columns))
				for i, column := range columns {
					switch column.role {
					case sqlColumnTimestamp:
						values[i] = formatTimestamp(point.Epoch)
					case sqlColumnTag:
						values[i] = sqlTagOutputValue(series.Header.Tags[column.name])
					case sqlColumnValue:
						if column.index < len(point.Values) {
							values[i] = point.Values[column.index]
						}
					}
				}
				rows = append(rows, sqlOutputRow{columns: columns, values: values, epoch: point.Epoch})
			}
		}
	}
	return rows
}

func sqlTagOutputValue(value string) any {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err == nil {
		return normalizeJSONValue(decoded)
	}
	return value
}

func validSQLColumnPositions(columns []sqlOutputColumn) bool {
	seen := make(map[int]struct{}, len(columns))
	for _, column := range columns {
		if column.pos < 0 {
			return false
		}
		if _, ok := seen[column.pos]; ok {
			return false
		}
		seen[column.pos] = struct{}{}
	}
	return true
}

func sortSQLRows(rows []sqlOutputRow, ordering []timeseries.OrderTerm) {
	if len(ordering) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b sqlOutputRow) int {
		for _, term := range ordering {
			av, at, aok := sqlRowValue(a, term.Column)
			bv, bt, bok := sqlRowValue(b, term.Column)
			if !aok || !bok {
				continue
			}
			if comparison, handled := compareSQLNulls(av, bv, term.NullsFirst); handled {
				if comparison != 0 {
					return comparison
				}
				continue
			}
			comparison := compareSQLValue(av, bv, at && bt)
			if comparison != 0 {
				if term.Descending {
					comparison = -comparison
				}
				return comparison
			}
		}
		return cmp.Compare(a.epoch, b.epoch)
	})
}

func compareSQLNulls(a, b any, nullsFirst bool) (int, bool) {
	aNil, bNil := a == nil, b == nil
	if !aNil && !bNil {
		return 0, false
	}
	if aNil && bNil {
		return 0, true
	}
	if aNil == nullsFirst {
		return -1, true
	}
	return 1, true
}

func sqlRowValue(row sqlOutputRow, name string) (any, bool, bool) {
	for i, column := range row.columns {
		if column.name == name || strings.EqualFold(column.name, name) {
			return row.values[i], column.role == sqlColumnTimestamp, true
		}
	}
	return nil, false, false
}

func compareSQLValue(a, b any, timestamp bool) int {
	if timestamp {
		return cmp.Compare(fmtSQLValue(a), fmtSQLValue(b))
	}
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
			aNaN, bNaN := math.IsNaN(av), math.IsNaN(bv)
			switch {
			case aNaN && bNaN:
				return 0
			case aNaN:
				return 1
			case bNaN:
				return -1
			default:
				return cmp.Compare(av, bv)
			}
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
	return cmp.Compare(fmtSQLValue(a), fmtSQLValue(b))
}

func fmtSQLValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

type sqlFlushWriter interface {
	io.Writer
	WriteString(string) (int, error)
	Flush() error
}

type sqlNopFlushWriter struct{ *bytes.Buffer }

func (sqlNopFlushWriter) Flush() error { return nil }

func writeSQLRow(writer sqlFlushWriter, row sqlOutputRow) error {
	columns := slices.Clone(row.columns)
	slices.SortStableFunc(columns, func(a, b sqlOutputColumn) int {
		return cmp.Compare(a.pos, b.pos)
	})
	if _, err := writer.WriteString("{"); err != nil {
		return err
	}
	for i, column := range columns {
		if i > 0 {
			if _, err := writer.WriteString(","); err != nil {
				return err
			}
		}
		key, err := json.Marshal(column.name)
		if err != nil {
			return err
		}
		if _, err := writer.Write(key); err != nil {
			return err
		}
		if _, err := writer.WriteString(":"); err != nil {
			return err
		}
		var value any
		for i, source := range row.columns {
			if source.name == column.name && i < len(row.values) {
				value = row.values[i]
				break
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := writer.Write(encoded); err != nil {
			return err
		}
	}
	_, err := writer.WriteString("}")
	return err
}
