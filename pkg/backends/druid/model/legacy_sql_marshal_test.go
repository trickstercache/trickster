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
	"cmp"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// the SQL marshaler that streams rows in order replaced, kept as its byte-exact oracle

type legacySqlOutputColumn struct {
	name  string
	role  byte
	index int
	pos   int
}

// a series' output layout: its columns, each tag column's value decoded once, each output key in
// position order with the column it writes, and each ordering term's column
type legacySqlSeries struct {
	columns []legacySqlOutputColumn
	tags    []any
	keys    []string
	sources []int
	order   []int
}

// one output row: where its series holds it, and the layout of its series
type legacySqlRow struct {
	s   *legacySqlSeries
	seg *dataset.Segment
	row int
}

func (r legacySqlRow) epoch() epoch.Epoch {
	return r.seg.Epoch(r.row)
}

// the row's value in column i; a time is formatted, which only rare comparisons need
func (r legacySqlRow) value(i int) any {
	c := &r.s.columns[i]
	switch c.role {
	case sqlColumnTimestamp:
		return formatTimestamp(r.epoch())
	case sqlColumnTag:
		return r.s.tags[i]
	}
	if c.index < r.seg.NumCols() {
		return r.seg.Value(c.index, r.row)
	}
	return nil
}

// appends the row's value in column i as JSON; the values were checked, so none fails
func (r legacySqlRow) appendValue(b []byte, i int) []byte {
	if i < 0 {
		return append(b, "null"...)
	}
	c := &r.s.columns[i]
	switch c.role {
	case sqlColumnTimestamp:
		return appendTimestamp(b, r.epoch())
	case sqlColumnTag:
		b, _ = tstrings.AppendJSONValue(b, r.s.tags[i])
		return b
	}
	if c.index < r.seg.NumCols() {
		b, _ = r.seg.AppendJSON(b, c.index, r.row)
		return b
	}
	return append(b, "null"...)
}

func legacyMarshalSQLTimeseriesWriter(ds *dataset.DataSet, marker *SQLQueryPlan,
	writer io.Writer,
) error {
	if marker == nil || marker.Plan == nil {
		return timeseries.ErrUnknownFormat
	}
	if hw, ok := writer.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	var ordering []timeseries.OrderTerm
	if ds != nil && ds.TimeRangeQuery != nil {
		ordering = ds.TimeRangeQuery.Ordering
	}
	rows := legacySqlOutputRows(ds, ordering)
	legacySortSQLRows(rows, ordering)
	if marker.ResponseFormat() == SQLResponseArray {
		return legacyMarshalSQLArrayRows(rows, marker, writer)
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	for _, row := range rows {
		if err := legacyCheckSQLRow(row, row.s.sources); err != nil {
			return err
		}
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, '[')
	for i, row := range rows {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, '{')
		for j, key := range row.s.keys {
			if j > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			cw.Buf = append(cw.Buf, key...)
			cw.Buf = row.appendValue(cw.Buf, row.s.sources[j])
		}
		cw.Buf = append(cw.Buf, '}')
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, "]\n"...)
	return cw.Close()
}

func legacyCheckSQLRow(row legacySqlRow, sources []int) error {
	for _, i := range sources {
		if c := &row.s.columns[i]; i >= 0 && c.role == sqlColumnValue && c.index < row.seg.NumCols() {
			if err := row.seg.CheckJSON(c.index, row.row); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyMarshalSQLArrayRows(rows []legacySqlRow, marker *SQLQueryPlan, writer io.Writer) error {
	if marker == nil || !marker.Header() {
		return timeseries.ErrUnknownFormat
	}
	var first []legacySqlOutputColumn
	if len(rows) > 0 {
		first = rows[0].s.columns
	}
	columns := legacySqlOutputColumns(first, marker)
	if hw, ok := writer.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	// each series' column for each output column: the first of that name, if any
	sources := map[*legacySqlSeries][]int{}
	for _, row := range rows {
		if _, ok := sources[row.s]; ok {
			continue
		}
		src := make([]int, len(columns))
		for i, column := range columns {
			src[i] = slices.IndexFunc(row.s.columns, func(c legacySqlOutputColumn) bool { return c.name == column.name })
		}
		sources[row.s] = src
		if err := legacyCheckSQLTags(row.s, src); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := legacyCheckSQLRow(row, sources[row.s]); err != nil {
			return err
		}
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, "[["...)
	for i, column := range columns {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = tstrings.AppendJSON(cw.Buf, column.name)
	}
	cw.Buf = append(cw.Buf, ']')
	for _, row := range rows {
		cw.Buf = append(cw.Buf, ",["...)
		for i, source := range sources[row.s] {
			if i > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			cw.Buf = row.appendValue(cw.Buf, source)
		}
		cw.Buf = append(cw.Buf, ']')
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, "]\n"...)
	return cw.Close()
}

// a series' tag values are decoded once, so they are checked once
func legacyCheckSQLTags(s *legacySqlSeries, sources []int) error {
	for _, i := range sources {
		if i >= 0 && s.columns[i].role == sqlColumnTag {
			if err := tstrings.CheckJSONValue(s.tags[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacySqlOutputColumns(first []legacySqlOutputColumn, marker *SQLQueryPlan) []legacySqlOutputColumn {
	if len(first) > 0 {
		columns := slices.Clone(first)
		slices.SortStableFunc(columns, func(a, b legacySqlOutputColumn) int {
			return cmp.Compare(a.pos, b.pos)
		})
		return columns
	}
	if marker == nil || marker.Plan == nil {
		return nil
	}
	if outputNames := marker.OutputColumns(); len(outputNames) > 0 {
		columns := make([]legacySqlOutputColumn, 0, len(outputNames))
		for pos, name := range outputNames {
			column := legacySqlOutputColumn{name: name, pos: pos}
			switch {
			case strings.EqualFold(name, marker.Plan.OutputColumn):
				column.role = sqlColumnTimestamp
			case legacySqlColumnNameIndex(marker.Plan.GroupColumns, name) >= 0:
				column.role = sqlColumnTag
				column.index = legacySqlColumnNameIndex(marker.Plan.GroupColumns, name)
			default:
				column.role = sqlColumnValue
				column.index = legacySqlColumnNameIndex(marker.Plan.ValueColumns, name)
			}
			columns = append(columns, column)
		}
		return columns
	}
	columns := make([]legacySqlOutputColumn, 0, 1+len(marker.Plan.GroupColumns)+len(marker.Plan.ValueColumns))
	columns = append(columns, legacySqlOutputColumn{name: marker.Plan.OutputColumn, role: sqlColumnTimestamp, pos: 0})
	for i, name := range marker.Plan.GroupColumns {
		columns = append(columns, legacySqlOutputColumn{name: name, role: sqlColumnTag, index: i, pos: i + 1})
	}
	for i, name := range marker.Plan.ValueColumns {
		columns = append(columns, legacySqlOutputColumn{name: name, role: sqlColumnValue, index: i, pos: i + 1 + len(marker.Plan.GroupColumns)})
	}
	return columns
}

func legacySqlColumnNameIndex(columns []string, name string) int {
	for i, column := range columns {
		if strings.EqualFold(column, name) {
			return i
		}
	}
	return -1
}

func legacySqlOutputRows(ds *dataset.DataSet, ordering []timeseries.OrderTerm) []legacySqlRow {
	if ds == nil {
		return nil
	}
	count := 0
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series != nil {
				count += series.PointCount()
			}
		}
	}
	rows := make([]legacySqlRow, 0, count)
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil || series.PointCount() == 0 {
				continue
			}
			s := legacyNewSQLSeries(series, ordering)
			segs := series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					rows = append(rows, legacySqlRow{s: s, seg: &segs[k], row: i})
				}
			}
		}
	}
	return rows
}

func legacyNewSQLSeries(series *dataset.Series, ordering []timeseries.OrderTerm) *legacySqlSeries {
	h := &series.Header
	columns := make([]legacySqlOutputColumn, 0, 1+len(h.TagFieldsList)+len(h.ValueFieldsList))
	timestamp := h.TimestampField
	if timestamp.Name == "" {
		timestamp.Name = "__time"
	}
	columns = append(columns, legacySqlOutputColumn{
		name: timestamp.Name, role: sqlColumnTimestamp, pos: timestamp.OutputPosition,
	})
	for i, field := range h.TagFieldsList {
		columns = append(columns, legacySqlOutputColumn{
			name: field.Name, role: sqlColumnTag, index: i, pos: field.OutputPosition,
		})
	}
	for i, field := range h.ValueFieldsList {
		columns = append(columns, legacySqlOutputColumn{
			name: field.Name, role: sqlColumnValue, index: i, pos: field.OutputPosition,
		})
	}
	// Synthetic DataSets often leave OutputPosition at zero. Preserve
	// the conventional SQL order in that case (timestamp, tags, values).
	if !legacyValidSQLColumnPositions(columns) {
		for i := range columns {
			columns[i].pos = i
		}
	}
	s := &legacySqlSeries{columns: columns, tags: make([]any, len(columns)), order: make([]int, len(ordering))}
	for i, column := range columns {
		if column.role == sqlColumnTag {
			s.tags[i] = legacySqlTagOutputValue(h.Tags[column.name])
		}
	}
	// keys in position order, each writing the first column of its name
	byPos := make([]int, len(columns))
	for i := range byPos {
		byPos[i] = i
	}
	slices.SortStableFunc(byPos, func(a, b int) int { return cmp.Compare(columns[a].pos, columns[b].pos) })
	s.keys, s.sources = make([]string, len(byPos)), make([]int, len(byPos))
	for j, i := range byPos {
		name := columns[i].name
		s.keys[j] = string(append(tstrings.AppendJSON(nil, name), ':'))
		s.sources[j] = slices.IndexFunc(columns, func(c legacySqlOutputColumn) bool { return c.name == name })
	}
	for t, term := range ordering {
		s.order[t] = slices.IndexFunc(columns, func(c legacySqlOutputColumn) bool {
			return c.name == term.Column || strings.EqualFold(c.name, term.Column)
		})
	}
	return s
}

func legacySqlTagOutputValue(value string) any {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err == nil {
		return normalizeJSONValue(decoded)
	}
	return value
}

func legacyValidSQLColumnPositions(columns []legacySqlOutputColumn) bool {
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

func legacySortSQLRows(rows []legacySqlRow, ordering []timeseries.OrderTerm) {
	if len(ordering) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b legacySqlRow) int {
		for t, term := range ordering {
			ai, bi := a.s.order[t], b.s.order[t]
			if ai < 0 || bi < 0 {
				continue
			}
			var comparison int
			if a.s.columns[ai].role == sqlColumnTimestamp && b.s.columns[bi].role == sqlColumnTimestamp {
				// times are never null; their text mixes millisecond and nanosecond forms, so compare epochs
				comparison = cmp.Compare(a.epoch(), b.epoch())
			} else {
				av, bv := a.value(ai), b.value(bi)
				if nulls, handled := legacyCompareSQLNulls(av, bv, term.NullsFirst); handled {
					if nulls != 0 {
						return nulls
					}
					continue
				}
				comparison = legacyCompareSQLValue(av, bv, false)
			}
			if comparison != 0 {
				if term.Descending {
					comparison = -comparison
				}
				return comparison
			}
		}
		return cmp.Compare(a.epoch(), b.epoch())
	})
}

func legacyCompareSQLNulls(a, b any, nullsFirst bool) (int, bool) {
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

func legacyCompareSQLValue(a, b any, timestamp bool) int {
	if timestamp {
		return cmp.Compare(legacyFmtSQLValue(a), legacyFmtSQLValue(b))
	}
	// the stream writer holds a number other than an int64 as its text, comparing it with any number
	_, af64 := a.(float64)
	_, bf64 := b.(float64)
	if (!legacyIsInt64(a) || !legacyIsInt64(b)) && !(af64 && bf64) {
		if af, ok := sqlNumber(a); ok {
			if bf, ok := sqlNumber(b); ok {
				return legacyCompareSQLValue(af, bf, false)
			}
		}
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
	return cmp.Compare(legacyFmtSQLValue(a), legacyFmtSQLValue(b))
}

func legacyFmtSQLValue(value any) string {
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

func legacyIsInt64(v any) bool {
	_, ok := v.(int64)
	return ok
}
