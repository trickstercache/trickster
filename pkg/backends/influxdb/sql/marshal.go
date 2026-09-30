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
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

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

// v3TimestampOutputLayout matches InfluxDB 3's native output shape: naive UTC
// with no zone suffix, fractional seconds only when present.
const v3TimestampOutputLayout = "2006-01-02T15:04:05.999999999"

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

// the row's value in column i: its formatted time, a tag, or a value, nil when the row has none
func (r v3Row) cell(i int) any {
	switch {
	case i == 0:
		return time.Unix(0, int64(r.epoch())).UTC().Format(v3TimestampOutputLayout)
	case i <= len(r.s.tags):
		return r.s.tags[i-1]
	}
	if j, ok := r.valueColumn(i); ok {
		return r.seg.Value(j, r.row)
	}
	return nil
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
	return writeRows(w, dataSetRows(ds), '[', ',', "]\n")
}

func marshalJSONL(w io.Writer, ds *dataset.DataSet) error {
	return writeRows(w, dataSetRows(ds), 0, '\n', "")
}

// writes rows as JSON objects in column order, which encoding/json's maps would sort, between open
// and closing, with sep after each but the last (JSON) or every one (JSONL)
func writeRows(w io.Writer, rows []v3Row, open, sep byte, closing string) error {
	if err := checkRowValues(rows); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(w)
	if open != 0 {
		cw.Buf = append(cw.Buf, open)
	}
	for i, row := range rows {
		if i > 0 && open != 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		cw.Buf = appendV3Object(cw.Buf, row)
		if open == 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, closing...)
	return cw.Close()
}

// nothing is written when a value can't be, as JSON has no NaN or infinities
func checkRowValues(rows []v3Row) error {
	for _, row := range rows {
		n := min(row.seg.NumCols(), len(row.s.columns)-1-len(row.s.tags))
		for j := range n {
			if err := row.seg.CheckJSON(j, row.row); err != nil {
				return err
			}
		}
	}
	return nil
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
			// the layout writes nothing JSON escapes
			b = append(b, '"')
			b = time.Unix(0, int64(row.epoch())).UTC().AppendFormat(b, v3TimestampOutputLayout)
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

func marshalCSV(w io.Writer, ds *dataset.DataSet) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	var lastColumns, record []string
	for _, row := range dataSetRows(ds) {
		// one header row per column layout; series sharing a layout share it
		if !slices.Equal(lastColumns, row.s.columns) {
			if err := cw.Write(row.s.columns); err != nil {
				return err
			}
			lastColumns = row.s.columns
		}
		record = record[:0]
		for i := range row.s.columns {
			record = append(record, formatValue(row.cell(i)))
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	return nil
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
