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
	"cmp"
	"encoding/json"
	"io"
	"iter"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
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

// a series' layout: its columns, tags decoded and as JSON, keys in position order with the columns they
// write, each ordering term's column, and whether time is written in milliseconds
type sqlSeries struct {
	columns []sqlOutputColumn
	tags    []any
	tagJSON []string
	keys    []string
	sources []int
	order   []int
	millis  bool
}

// one output row: where its series holds it, and the layout of its series
type sqlRow struct {
	s   *sqlSeries
	seg *dataset.Segment
	row int
}

func (r sqlRow) epoch() epoch.Epoch {
	return r.seg.Epoch(r.row)
}

// the row's value in column i; a time is formatted, which only rare comparisons need
func (r sqlRow) value(i int) any {
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

// appends the row's value in column i as Druid wrote it; the values were checked, so none fails
func (r sqlRow) appendValue(b []byte, i int, tt *timeText) []byte {
	if i < 0 {
		return append(b, "null"...)
	}
	c := &r.s.columns[i]
	switch c.role {
	case sqlColumnTimestamp:
		if r.s.millis {
			return strconv.AppendInt(b, int64(r.epoch())/int64(time.Millisecond), 10)
		}
		return tt.append(b, r.epoch())
	case sqlColumnTag:
		return append(b, r.s.tagJSON[i]...)
	}
	if c.index < r.seg.NumCols() {
		b, _ = appendDruidValue(b, r.seg, c.index, r.row)
		return b
	}
	return append(b, "null"...)
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
	var ordering []timeseries.OrderTerm
	if ds != nil && ds.TimeRangeQuery != nil {
		ordering = ds.TimeRangeQuery.Ordering
	}
	o := newSQLOrder(ds, ordering)
	if marker.ResponseFormat() == SQLResponseArray {
		return marshalSQLArrayRows(o, marker, writer)
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	for _, s := range o.series {
		if err := checkSQLSeries(s, o.layouts[s], o.layouts[s].sources); err != nil {
			return err
		}
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, '[')
	var tt timeText
	i := 0
	for row := range o.rows() {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, '{')
		for j, key := range row.s.keys {
			if j > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			cw.Buf = append(cw.Buf, key...)
			cw.Buf = row.appendValue(cw.Buf, row.s.sources[j], &tt)
		}
		cw.Buf = append(cw.Buf, '}')
		cw.FlushIfFull()
		i++
	}
	cw.Buf = append(cw.Buf, "]\n"...)
	return cw.Close()
}

// checkSQLSeries checks every value a series' rows write, a column at a time
func checkSQLSeries(series *dataset.Series, layout *sqlSeries, sources []int) error {
	segs := series.Segments()
	for _, i := range sources {
		if i < 0 || layout.columns[i].role != sqlColumnValue {
			continue
		}
		for k := range segs {
			if c := layout.columns[i].index; c < segs[k].NumCols() {
				if err := checkDruidColumn(&segs[k], c); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func marshalSQLArrayRows(o *sqlOrder, marker *SQLQueryPlan, writer io.Writer) error {
	if marker == nil || !marker.Header() {
		return timeseries.ErrUnknownFormat
	}
	var first []sqlOutputColumn
	for row := range o.rows() {
		first = row.s.columns
		break
	}
	columns := sqlOutputColumns(first, marker)
	if hw, ok := writer.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	// each series' column for each output column: the first of that name, if any
	sources := make(map[*sqlSeries][]int, len(o.layouts))
	for _, s := range o.series {
		layout := o.layouts[s]
		src := make([]int, len(columns))
		for i, column := range columns {
			src[i] = slices.IndexFunc(layout.columns, func(c sqlOutputColumn) bool { return c.name == column.name })
		}
		sources[layout] = src
		if err := checkSQLSeries(s, layout, src); err != nil {
			return err
		}
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, "[["...)
	for i, column := range columns {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = appendJacksonString(cw.Buf, column.name)
	}
	cw.Buf = append(cw.Buf, ']')
	var tt timeText
	for row := range o.rows() {
		cw.Buf = append(cw.Buf, ",["...)
		for i, source := range sources[row.s] {
			if i > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			cw.Buf = row.appendValue(cw.Buf, source, &tt)
		}
		cw.Buf = append(cw.Buf, ']')
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, "]\n"...)
	return cw.Close()
}

func sqlOutputColumns(first []sqlOutputColumn, marker *SQLQueryPlan) []sqlOutputColumn {
	if len(first) > 0 {
		columns := slices.Clone(first)
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

// sqlOrder yields a DataSet's rows in the query's order, with each series' layout
type sqlOrder struct {
	ds       *dataset.DataSet
	ordering []timeseries.OrderTerm
	// each series' layout, by series and by its position in the one result a time-first order merges
	layouts map[*dataset.Series]*sqlSeries
	series  []*dataset.Series
	merged  []*sqlSeries
	result  *dataset.Result
	// whether the terms after time are all tags, which the merged series are sorted by
	byTags bool
}

func newSQLOrder(ds *dataset.DataSet, ordering []timeseries.OrderTerm) *sqlOrder {
	o := &sqlOrder{ds: ds, ordering: ordering, layouts: map[*dataset.Series]*sqlSeries{}}
	if ds == nil {
		return o
	}
	results := 0
	var layouts sqlLayouts
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		results++
		o.result = result
		for _, series := range result.SeriesList {
			if series != nil && series.PointCount() > 0 {
				o.layouts[series] = layouts.series(series, ordering)
				o.series = append(o.series, series)
			}
		}
	}
	// a single result of sorted series, ordered by time first, merges in order
	if results != 1 || len(ordering) == 0 {
		o.result = nil
		return o
	}
	o.merged = make([]*sqlSeries, len(o.result.SeriesList))
	o.byTags = true
	for i, series := range o.result.SeriesList {
		layout := o.layouts[series]
		if layout == nil {
			continue
		}
		if !series.IsSorted() || layout.order[0] != 0 {
			o.result = nil
			return o
		}
		o.merged[i] = layout
		for _, c := range layout.order[1:] {
			o.byTags = o.byTags && (c < 0 || layout.columns[c].role == sqlColumnTag)
		}
	}
	if o.byTags {
		o.sortByTags()
	}
	return o
}

// sortByTags orders a copy of the result's series as the order's terms after time order their rows,
// which are all tags, so the rows of an epoch merge in series order
func (o *sqlOrder) sortByTags() {
	idx := make([]int, len(o.result.SeriesList))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		// a series without rows has no layout, and sorts last
		la, lb := o.merged[a], o.merged[b]
		if la == nil || lb == nil {
			return cmp.Compare(boolInt(la == nil), boolInt(lb == nil))
		}
		return compareSQLTerms(sqlRow{s: la}, sqlRow{s: lb}, o.ordering[1:], 1)
	})
	sorted := &dataset.Result{
		StatementID: o.result.StatementID, Name: o.result.Name,
		SeriesList: make(dataset.SeriesList, len(idx)),
	}
	merged := make([]*sqlSeries, len(idx))
	for i, j := range idx {
		sorted.SeriesList[i], merged[i] = o.result.SeriesList[j], o.merged[j]
	}
	o.result, o.merged = sorted, merged
}

// rows yields the rows: as stored without an order, merged by time when the order starts with it, and
// otherwise sorted
func (o *sqlOrder) rows() iter.Seq[sqlRow] {
	return func(yield func(sqlRow) bool) {
		if o.ds == nil {
			return
		}
		if len(o.ordering) == 0 || o.result == nil {
			rows := o.stored()
			sortSQLRows(rows, o.ordering)
			for _, row := range rows {
				if !yield(row) {
					return
				}
			}
			return
		}
		order := dataset.RowOrder{Descending: o.ordering[0].Descending}
		switch {
		case o.byTags && order.Descending:
			// descending reads each series backward, so rows tied on every term need their stored order
			order.Compare = compareStoredRows
		case o.byTags:
		case len(o.ordering) > 1 || order.Descending:
			order.Compare = func(a, b dataset.Row) int {
				ra := sqlRow{s: o.merged[a.SeriesIndex], seg: a.Seg, row: a.Index}
				rb := sqlRow{s: o.merged[b.SeriesIndex], seg: b.Seg, row: b.Index}
				if c := compareSQLTerms(ra, rb, o.ordering[1:], 1); c != 0 {
					return c
				}
				return compareStoredRows(a, b)
			}
		}
		for row := range o.result.Rows(order) {
			if !yield(sqlRow{s: o.merged[row.SeriesIndex], seg: row.Seg, row: row.Index}) {
				return
			}
		}
	}
}

// stored returns the rows as the results and series hold them
func (o *sqlOrder) stored() []sqlRow {
	count := 0
	for series := range o.layouts {
		count += series.PointCount()
	}
	rows := make([]sqlRow, 0, count)
	for _, result := range o.ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			s := o.layouts[series]
			if s == nil {
				continue
			}
			segs := series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					rows = append(rows, sqlRow{s: s, seg: &segs[k], row: i})
				}
			}
		}
	}
	return rows
}

// compareStoredRows orders rows as their series, and within one its Segments and rows, hold them
func compareStoredRows(a, b dataset.Row) int {
	if c := cmp.Compare(a.SeriesIndex, b.SeriesIndex); c != 0 {
		return c
	}
	if a.Seg != b.Seg {
		segs := a.Series.Segments()
		for k := range segs {
			switch &segs[k] {
			case a.Seg:
				return -1
			case b.Seg:
				return 1
			}
		}
	}
	return cmp.Compare(a.Index, b.Index)
}

// sqlLayouts lays out series, sharing the last layout's columns and keys with a series of its fields
type sqlLayouts struct {
	header *dataset.SeriesHeader
	last   *sqlSeries
}

func (l *sqlLayouts) series(series *dataset.Series, ordering []timeseries.OrderTerm) *sqlSeries {
	h := &series.Header
	var s *sqlSeries
	if l.last != nil && sameSQLFields(l.header, h) {
		shared := *l.last
		s = &shared
	} else {
		s = newSQLSeries(h, ordering)
	}
	s.tags, s.tagJSON = make([]any, len(s.columns)), make([]string, len(s.columns))
	for i, column := range s.columns {
		if column.role == sqlColumnTag {
			s.tags[i], s.tagJSON[i] = sqlTagOutputValue(h.Tags[column.name])
		}
	}
	l.header, l.last = h, s
	return s
}

func sameSQLFields(a, b *dataset.SeriesHeader) bool {
	at, bt := &a.TimestampField, &b.TimestampField
	return at.Name == bt.Name && at.OutputPosition == bt.OutputPosition && at.DataType == bt.DataType &&
		sameSQLFieldList(a.TagFieldsList, b.TagFieldsList) && sameSQLFieldList(a.ValueFieldsList, b.ValueFieldsList)
}

func sameSQLFieldList(a, b timeseries.FieldDefinitions) bool {
	return slices.EqualFunc(a, b, func(x, y timeseries.FieldDefinition) bool {
		return x.Name == y.Name && x.OutputPosition == y.OutputPosition
	})
}

// newSQLSeries returns the layout of a series of header h, but its tags
func newSQLSeries(h *dataset.SeriesHeader, ordering []timeseries.OrderTerm) *sqlSeries {
	columns := make([]sqlOutputColumn, 0, 1+len(h.TagFieldsList)+len(h.ValueFieldsList))
	timestamp := h.TimestampField
	if timestamp.Name == "" {
		timestamp.Name = "__time"
	}
	columns = append(columns, sqlOutputColumn{
		name: timestamp.Name, role: sqlColumnTimestamp, pos: timestamp.OutputPosition,
	})
	for i, field := range h.TagFieldsList {
		columns = append(columns, sqlOutputColumn{
			name: field.Name, role: sqlColumnTag, index: i, pos: field.OutputPosition,
		})
	}
	for i, field := range h.ValueFieldsList {
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
	s := &sqlSeries{
		columns: columns, order: make([]int, len(ordering)),
		millis: timestamp.DataType == timeseries.DateTimeUnixMilli,
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
		s.keys[j] = string(append(appendJacksonString(nil, name), ':'))
		s.sources[j] = slices.IndexFunc(columns, func(c sqlOutputColumn) bool { return c.name == name })
	}
	for t, term := range ordering {
		s.order[t] = slices.IndexFunc(columns, func(c sqlOutputColumn) bool {
			return c.name == term.Column || strings.EqualFold(c.name, term.Column)
		})
	}
	return s
}

// sqlTagOutputValue returns a tag's value, which a decoded tag holds as the JSON Druid wrote, and the
// JSON written of it: that JSON, or a tag that isn't JSON as a string
func sqlTagOutputValue(value string) (any, string) {
	switch value {
	case jsonNullText:
		return nil, value
	case jsonTrue:
		return true, value
	case jsonFalse:
		return false, value
	}
	if text, ok := plainJSONString(value); ok {
		return text, value
	}
	if i, ok := jsonInteger(value); ok {
		return i, value
	}
	if raw := []byte(value); json.Valid(raw) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		// raw is valid, so it decodes
		_ = decoder.Decode(&decoded)
		return normalizeJSONValue(decoded), value
	}
	return value, string(appendJacksonString(nil, value))
}

// plainJSONString returns the text of a JSON string that escapes nothing
func plainJSONString(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	text := s[1 : len(s)-1]
	for i := range len(text) {
		if c := text[i]; c < 0x20 || c == '"' || c == '\\' {
			return "", false
		}
	}
	return text, utf8.ValidString(text)
}

// jsonInteger returns the value of a JSON integer that an int64 holds
func jsonInteger(s string) (int64, bool) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || digits[0] < '0' || digits[0] > '9' || (digits[0] == '0' && len(digits) > 1) {
		return 0, false
	}
	i, err := strconv.ParseInt(s, 10, 64)
	return i, err == nil
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

func sortSQLRows(rows []sqlRow, ordering []timeseries.OrderTerm) {
	if len(ordering) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b sqlRow) int {
		if c := compareSQLTerms(a, b, ordering, 0); c != 0 {
			return c
		}
		return cmp.Compare(a.epoch(), b.epoch())
	})
}

// compareSQLTerms orders two rows by the ordering's terms, the first being term first of the order
func compareSQLTerms(a, b sqlRow, ordering []timeseries.OrderTerm, first int) int {
	for i, term := range ordering {
		t := first + i
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
			if nulls, handled := compareSQLNulls(av, bv, term.NullsFirst); handled {
				if nulls != 0 {
					return nulls
				}
				continue
			}
			comparison = compareSQLValue(av, bv, false)
		}
		if comparison != 0 {
			if term.Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	return 0
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

func compareSQLValue(a, b any, timestamp bool) int {
	if timestamp {
		return cmp.Compare(fmtSQLValue(a), fmtSQLValue(b))
	}
	// a number other than an int64 is held as its text, and compares with any number by value
	if an, bn := isSQLNumberText(a), isSQLNumberText(b); an || bn {
		if af, aok := sqlNumber(a); aok {
			if bf, bok := sqlNumber(b); bok {
				return compareSQLValue(af, bf, false)
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
	return cmp.Compare(fmtSQLValue(a), fmtSQLValue(b))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isSQLNumberText(value any) bool {
	_, ok := value.(json.Number)
	return ok
}

// sqlNumber returns a number value as a float64
func sqlNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	}
	return 0, false
}

func fmtSQLValue(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}
