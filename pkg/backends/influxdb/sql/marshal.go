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
	"bytes"
	"cmp"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
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

// a series' output layout: its columns, each one's JSON key, its tag values, and the column each
// ordering term sorts by (-1 when it has none)
type v3Series struct {
	columns []string
	keys    []string
	tags    []string
	order   []int
}

// one output row: where its series holds it, and the layout of its series
type v3Row struct {
	s   *v3Series
	seg *dataset.Segment
	row int
}

// dataSetRows flattens a DataSet into output rows: the timestamp column,
// then the series' tag columns, then value columns, in header order.
func dataSetRows(ds *dataset.DataSet) []v3Row {
	var ordering []timeseries.OrderTerm
	if ds.TimeRangeQuery != nil {
		ordering = ds.TimeRangeQuery.Ordering
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
	rows := make([]v3Row, 0, count)
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil || series.PointCount() == 0 {
				continue
			}
			s := newV3Series(series, ordering)
			segs := series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					rows = append(rows, v3Row{s: s, seg: &segs[k], row: i})
				}
			}
		}
	}
	sortV3Rows(rows, ordering)
	return rows
}

func newV3Series(series *dataset.Series, ordering []timeseries.OrderTerm) *v3Series {
	h := &series.Header
	tsName := h.TimestampField.Name
	if tsName == "" {
		tsName = DefaultTimestampField
	}
	n := 1 + len(h.TagFieldsList) + len(h.ValueFieldsList)
	s := &v3Series{
		columns: make([]string, 0, n), keys: make([]string, 0, n),
		tags: make([]string, len(h.TagFieldsList)), order: make([]int, len(ordering)),
	}
	s.columns = append(s.columns, tsName)
	for i, fd := range h.TagFieldsList {
		s.columns = append(s.columns, fd.Name)
		s.tags[i] = h.Tags[fd.Name]
	}
	for _, fd := range h.ValueFieldsList {
		s.columns = append(s.columns, fd.Name)
	}
	for _, name := range s.columns {
		s.keys = append(s.keys, string(append(tstrings.AppendJSON(nil, name), ':')))
	}
	for t, term := range ordering {
		s.order[t] = slices.Index(s.columns, term.Column)
	}
	return s
}

func (r v3Row) epoch() epoch.Epoch {
	return r.seg.Epoch(r.row)
}

// the value column of the row's column i, and whether the row has it
func (r v3Row) valueColumn(i int) (int, bool) {
	j := i - 1 - len(r.s.tags)
	return j, j < r.seg.NumCols()
}

// v3Cell is a row's tag or value in one column, read without boxing where its kind allows
type v3Cell struct {
	kind  dataset.Kind
	text  string
	f     float64
	i     int64
	u     uint64
	b     bool
	other any
}

func (r v3Row) typedCell(i int) v3Cell {
	if i <= len(r.s.tags) {
		return v3Cell{kind: dataset.KindString, text: r.s.tags[i-1]}
	}
	j, ok := r.valueColumn(i)
	if !ok {
		return v3Cell{kind: dataset.KindNull}
	}
	switch k := r.seg.KindAt(j, r.row); k {
	case dataset.KindNull:
		return v3Cell{kind: k}
	case dataset.KindString:
		return v3Cell{kind: k, text: r.seg.Text(j, r.row)}
	case dataset.KindFloat64:
		return v3Cell{kind: k, f: r.seg.Float64(j, r.row)}
	case dataset.KindInt64:
		return v3Cell{kind: k, i: r.seg.Int64(j, r.row)}
	case dataset.KindUint64:
		return v3Cell{kind: k, u: r.seg.Uint64(j, r.row)}
	case dataset.KindBool:
		return v3Cell{kind: k, b: r.seg.Bool(j, r.row)}
	}
	return v3Cell{kind: dataset.KindExt, other: r.seg.Value(j, r.row)}
}

// boxed returns the cell's value as the row holds it
func (c v3Cell) boxed() any {
	switch c.kind {
	case dataset.KindString:
		return c.text
	case dataset.KindFloat64:
		return c.f
	case dataset.KindInt64:
		return c.i
	case dataset.KindUint64:
		return c.u
	case dataset.KindBool:
		return c.b
	}
	return c.other
}

func sortV3Rows(rows []v3Row, ordering []timeseries.OrderTerm) {
	if len(rows) < 2 || len(ordering) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b v3Row) int {
		for t, term := range ordering {
			comparison := compareV3Row(a, b, t, term)
			if comparison == 0 {
				continue
			}
			return comparison
		}
		return 0
	})
}

func compareV3Row(a, b v3Row, t int, term timeseries.OrderTerm) int {
	ai, bi := a.s.order[t], b.s.order[t]
	if ai < 0 || bi < 0 {
		return 0
	}
	if ai == 0 && bi == 0 {
		// times, which are never null, compare as instants
		return applyV3Direction(cmp.Compare(a.epoch(), b.epoch()), term.Descending)
	}
	av, bv := a.typedCell(ai), b.typedCell(bi)
	nullA, nullB := av.kind == dataset.KindNull, bv.kind == dataset.KindNull
	if nullA || nullB {
		switch {
		case nullA && nullB:
			return 0
		case nullA && term.NullsFirst:
			return -1
		case nullA:
			return 1
		case term.NullsFirst:
			return 1
		default:
			return -1
		}
	}
	return applyV3Direction(compareV3Cells(av, bv), term.Descending)
}

// compareV3Cells compares two values of one kind directly, and any others as compareV3Value does
func compareV3Cells(a, b v3Cell) int {
	if a.kind == b.kind {
		switch a.kind {
		case dataset.KindString:
			return cmp.Compare(a.text, b.text)
		case dataset.KindInt64:
			return cmp.Compare(a.i, b.i)
		case dataset.KindUint64:
			return cmp.Compare(a.u, b.u)
		case dataset.KindFloat64:
			return compareV3Floats(a.f, b.f)
		case dataset.KindBool:
			return compareV3Bools(a.b, b.b)
		}
	}
	return compareV3Value(a.boxed(), b.boxed())
}

// NaN sorts after every number, as compareV3Value orders floats
func compareV3Floats(a, b float64) int {
	switch aNaN, bNaN := math.IsNaN(a), math.IsNaN(b); {
	case aNaN && bNaN:
		return 0
	case aNaN:
		return 1
	case bNaN:
		return -1
	}
	return cmp.Compare(a, b)
}

func compareV3Bools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

func applyV3Direction(comparison int, descending bool) int {
	if descending {
		return -comparison
	}
	return comparison
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
			return compareV3Floats(av, bv)
		}
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv)
		}
	case bool:
		if bv, ok := b.(bool); ok {
			return compareV3Bools(av, bv)
		}
	}
	return cmp.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func marshalJSON(w io.Writer, ds *dataset.DataSet) error {
	return writeRows(w, ds, '[', ',', "]\n")
}

func marshalJSONL(w io.Writer, ds *dataset.DataSet) error {
	return writeRows(w, ds, 0, '\n', "")
}

// writes the rows as JSON objects in column order, which encoding/json's maps would sort, between open
// and closing, with sep after each but the last (JSON) or every one (JSONL)
func writeRows(w io.Writer, ds *dataset.DataSet, open, sep byte, closing string) error {
	if err := checkValues(ds); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(w)
	if open != 0 {
		cw.Buf = append(cw.Buf, open)
	}
	first := true
	eachRow(ds, func(row v3Row) {
		if !first && open != 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		first = false
		cw.Buf = appendV3Object(cw.Buf, row)
		if open == 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		cw.FlushIfFull()
	})
	cw.Buf = append(cw.Buf, closing...)
	return cw.Close()
}

// nothing is written when a value can't be, as JSON has no NaN or infinities
func checkValues(ds *dataset.DataSet) error {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			// only the value columns a row's layout names are written
			values := len(series.Header.ValueFieldsList)
			segs := series.Segments()
			for k := range segs {
				for c := range min(segs[k].NumCols(), values) {
					if err := segs[k].CheckColumnJSON(c); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// eachRow calls fn with each output row in the query's order: merged from the series as they're
// read when the order starts with time, as stored without an order, and otherwise sorted
func eachRow(ds *dataset.DataSet, fn func(v3Row)) {
	var ordering []timeseries.OrderTerm
	if ds.TimeRangeQuery != nil {
		ordering = ds.TimeRangeQuery.Ordering
	}
	if len(ordering) == 0 {
		for _, result := range ds.Results {
			if result == nil {
				continue
			}
			for _, series := range result.SeriesList {
				if series == nil || series.PointCount() == 0 {
					continue
				}
				s := newV3Series(series, nil)
				segs := series.Segments()
				for k := range segs {
					for i := range segs[k].Len() {
						fn(v3Row{s: s, seg: &segs[k], row: i})
					}
				}
			}
		}
		return
	}
	if r, layouts, ok := timeOrdered(ds, ordering); ok {
		order := dataset.RowOrder{Descending: ordering[0].Descending}
		// descending reads each series backward, so rows tied on every term need their stored order
		if len(ordering) > 1 || order.Descending {
			order.Compare = func(a, b dataset.Row) int {
				ra := v3Row{s: layouts[a.SeriesIndex], seg: a.Seg, row: a.Index}
				rb := v3Row{s: layouts[b.SeriesIndex], seg: b.Seg, row: b.Index}
				for t := 1; t < len(ordering); t++ {
					if c := compareV3Row(ra, rb, t, ordering[t]); c != 0 {
						return c
					}
				}
				return dataset.CompareStored(a, b)
			}
		}
		for row := range r.Rows(order) {
			fn(v3Row{s: layouts[row.SeriesIndex], seg: row.Seg, row: row.Index})
		}
		return
	}
	for _, row := range dataSetRows(ds) {
		fn(row)
	}
}

// timeOrdered returns the one result a time-first order can merge in order, and its series' layouts,
// or false when there are others or a series isn't sorted
func timeOrdered(ds *dataset.DataSet, ordering []timeseries.OrderTerm) (*dataset.Result, []*v3Series, bool) {
	var r *dataset.Result
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		if r != nil {
			return nil, nil, false
		}
		r = result
	}
	if r == nil {
		return nil, nil, false
	}
	layouts := make([]*v3Series, len(r.SeriesList))
	for i, series := range r.SeriesList {
		if series == nil || series.PointCount() == 0 {
			continue
		}
		if !series.IsSorted() {
			return nil, nil, false
		}
		// the time is each layout's first column
		if layouts[i] = newV3Series(series, ordering); layouts[i].order[0] != 0 {
			return nil, nil, false
		}
	}
	return r, layouts, true
}

func appendV3Object(b []byte, row v3Row) []byte {
	s := row.s
	b = append(b, '{')
	for i, key := range s.keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, key...)
		switch {
		case i == 0:
			// InfluxDB 3's own time: naive UTC, with a fraction only when it has one, which JSON
			// doesn't escape
			b = append(b, '"')
			b = epoch.AppendCanonicalTime(b, row.epoch(), true, false)
			b = append(b, '"')
		case i <= len(s.tags):
			b = tstrings.AppendJSON(b, s.tags[i-1])
		default:
			// the values were checked, so none fails
			if j, ok := row.valueColumn(i); ok {
				b, _ = row.seg.AppendJSON(b, j, row.row)
			} else {
				b = append(b, "null"...)
			}
		}
	}
	return append(b, '}')
}

// writes the rows as encoding/csv's Writer writes them, with a header record for each column layout
func marshalCSV(w io.Writer, ds *dataset.DataSet) error {
	cw := tbytes.NewChunkWriter(w)
	var last *v3Series
	var lastColumns []string
	eachRow(ds, func(row v3Row) {
		// series sharing a layout share its header
		if row.s != last {
			if !slices.Equal(lastColumns, row.s.columns) {
				for i, name := range row.s.columns {
					if i > 0 {
						cw.Buf = append(cw.Buf, ',')
					}
					cw.Buf = tstrings.AppendCSVField(cw.Buf, name, ',')
				}
				cw.Buf = append(cw.Buf, '\n')
				lastColumns = row.s.columns
			}
			last = row.s
		}
		for i := range row.s.columns {
			if i > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			cw.Buf = appendCSVCell(cw.Buf, row, i)
		}
		cw.Buf = append(cw.Buf, '\n')
		cw.FlushIfFull()
	})
	return cw.Close()
}

// appendCSVCell appends the row's column i as fmt's %v writes it, and a float without an exponent
func appendCSVCell(b []byte, row v3Row, i int) []byte {
	switch {
	case i == 0:
		// InfluxDB 3's own time, which CSV doesn't quote
		return epoch.AppendCanonicalTime(b, row.epoch(), true, false)
	case i <= len(row.s.tags):
		return tstrings.AppendCSVField(b, row.s.tags[i-1], ',')
	}
	j, ok := row.valueColumn(i)
	if !ok {
		return b
	}
	switch row.seg.KindAt(j, row.row) {
	case dataset.KindNull:
		return b
	case dataset.KindString:
		return tstrings.AppendCSVField(b, row.seg.Text(j, row.row), ',')
	case dataset.KindFloat64:
		return strconv.AppendFloat(b, row.seg.Float64(j, row.row), 'f', -1, 64)
	case dataset.KindInt64:
		return strconv.AppendInt(b, row.seg.Int64(j, row.row), 10)
	case dataset.KindBool:
		return strconv.AppendBool(b, row.seg.Bool(j, row.row))
	}
	return tstrings.AppendCSVField(b, fmt.Sprint(row.seg.Value(j, row.row)), ',')
}
