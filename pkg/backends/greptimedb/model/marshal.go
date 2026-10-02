/*
 * Copyright 2026 The Trickster Authors
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
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

var jsonNull = []byte("null")

func MarshalTimeseries(ts timeseries.Timeseries, options *timeseries.RequestOptions, status int) ([]byte, error) {
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(ts, options, status, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func MarshalTimeseriesWriter(ts timeseries.Timeseries, _ *timeseries.RequestOptions, _ int, w io.Writer) error {
	d, ok := ts.(*dataSet)
	if !ok || d == nil || d.DataSet == nil || d.invalid || len(d.fields) == 0 || w == nil {
		return timeseries.ErrInvalidBody
	}
	p, err := newGreptimePlan(d)
	if err != nil {
		return err
	}
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set("Content-Type", "application/json")
		hw.Header().Set("X-Greptime-Format", "greptimedb_v1")
		hw.Header().Set("X-Greptime-Execution-Time", "0")
		hw.Header().Del("X-Greptime-Metrics")
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	if err := p.checkValues(); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(w)
	cw.Buf = append(cw.Buf, `{"output":[{"records":{"schema":{"column_schemas":[`...)
	for i := range d.fields {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, `{"name":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, d.fields[i].Name)
		cw.Buf = append(cw.Buf, `,"data_type":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, d.fields[i].SDataType)
		cw.Buf = append(cw.Buf, '}')
	}
	cw.Buf = append(cw.Buf, `]},"rows":[`...)
	var n int64
	for row := range p.rows() {
		if n > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = p.appendRow(cw.Buf, row)
		n++
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, `],"total_rows":`...)
	cw.Buf = strconv.AppendInt(cw.Buf, n, 10)
	cw.Buf = append(cw.Buf, "}}],\"execution_time_ms\":0}\n"...)
	return cw.Close()
}

// a series' tag values, decoded once, and every non-time, non-value column as its JSON (null by
// default), which each of its rows shares
type greptimeSeries struct {
	tags  []any
	cells [][]byte
}

// one output row: where its series holds it, and its series
type greptimeRow struct {
	s   *greptimeSeries
	seg *dataset.Segment
	row int
}

func (r greptimeRow) epoch() epoch.Epoch {
	return r.seg.Epoch(r.row)
}

// an ORDER BY term and the field it names
type orderColumn struct {
	timeseries.OrderTerm
	index int
}

// greptimePlan writes a dataset's rows: each series' shared cells, laid out once, in the query's order
type greptimePlan struct {
	d      *dataSet
	fields timeseries.FieldDefinitions
	// per field: its value column, or -1, and a time's scale
	valueIndex []int
	scales     []int64
	order      []orderColumn
	// per result, per series
	layouts [][]*greptimeSeries
}

// newGreptimePlan lays out the dataset's series, failing as writing them field by field would
func newGreptimePlan(d *dataSet) (*greptimePlan, error) {
	n := len(d.fields)
	p := &greptimePlan{d: d, fields: d.fields, valueIndex: make([]int, n), scales: make([]int64, n)}
	var valueFields, timeFields []int
	for i := range d.fields {
		p.valueIndex[i] = -1
		switch d.fields[i].Role {
		case timeseries.RoleValue:
			p.valueIndex[i] = len(valueFields)
			valueFields = append(valueFields, i)
		case timeseries.RoleTimestamp:
			p.scales[i] = axisScale(d.fields[i])
			timeFields = append(timeFields, i)
		}
	}
	if d.TimeRangeQuery != nil {
		for _, term := range d.TimeRangeQuery.Ordering {
			if index := slices.IndexFunc(d.fields, func(field timeseries.FieldDefinition) bool {
				return field.Name == term.Column
			}); index >= 0 {
				p.order = append(p.order, orderColumn{term, index})
			}
		}
	}
	count := 0
	for _, result := range d.Results {
		if result != nil {
			count += len(result.SeriesList)
		}
	}
	// every layout's cells share these slabs
	all := make([]greptimeSeries, 0, count)
	tags, cells := make([]any, count*n), make([][]byte, count*n)
	p.layouts = make([][]*greptimeSeries, len(d.Results))
	for ri, result := range d.Results {
		if result == nil {
			continue
		}
		p.layouts[ri] = make([]*greptimeSeries, len(result.SeriesList))
		for si, series := range result.SeriesList {
			if series == nil {
				continue
			}
			all = append(all, greptimeSeries{tags: tags[:n:n], cells: cells[:n:n]})
			tags, cells = tags[n:], cells[n:]
			s := &all[len(all)-1]
			if err := p.layout(s, series); err != nil {
				return nil, err
			}
			segs := series.Segments()
			for k := range segs {
				if err := p.checkSegment(&segs[k], timeFields, valueFields); err != nil {
					return nil, err
				}
			}
			p.layouts[ri][si] = s
		}
	}
	return p, nil
}

// layout decodes the series' tags, which hold each value's JSON, and lays out its shared cells
func (p *greptimePlan) layout(s *greptimeSeries, series *dataset.Series) error {
	for i := range p.fields {
		field := &p.fields[i]
		if field.Role != timeseries.RoleTag {
			s.cells[i] = jsonNull
			continue
		}
		encoded, ok := series.Header.Tags[field.Name]
		if !ok {
			return timeseries.ErrInvalidBody
		}
		v, err := decodeTag(encoded, field.SDataType)
		if err != nil {
			return err
		}
		if s.cells[i], err = tstrings.AppendJSONValue(nil, v); err != nil {
			return err
		}
		s.tags[i] = v
	}
	return nil
}

// decodeTag decodes a tag's JSON as encoding/json does, skipping the decoder for a null or a plain string
func decodeTag(encoded, typ string) (any, error) {
	if encoded == "null" {
		return nil, nil
	}
	if typ == "String" && plainJSONString(encoded) {
		return encoded[1 : len(encoded)-1], nil
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return decodeValue(value, typ)
}

// plainJSONString reports whether s is a JSON string of valid UTF-8 without escapes, which is its own text
func plainJSONString[T string | []byte](s T) bool {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	inner := s[1 : len(s)-1]
	for i := range len(inner) {
		if c := inner[i]; c < 0x20 || c == '"' || c == '\\' {
			return false
		}
	}
	return utf8.ValidString(string(inner))
}

// checkSegment fails as writing the rows field by field would: at a bad time, or at a missing value's
// field if that's first, or after all fields when values are left over
func (p *greptimePlan) checkSegment(seg *dataset.Segment, timeFields, valueFields []int) error {
	rows := seg.Len()
	if rows == 0 {
		return nil
	}
	stop, mismatched := len(p.fields), seg.NumCols() != len(valueFields)
	if n := seg.NumCols(); n < len(valueFields) {
		stop = valueFields[n]
	}
	// every row fails alike on a missing or extra value, so only the first row's times precede it
	if mismatched {
		rows = 1
	}
	for _, i := range timeFields {
		if i >= stop {
			break
		}
		field := &p.fields[i]
		switch {
		case p.scales[i] == 0:
			return timeseries.ErrInvalidTimeFormat
		// a float time, or a signed one of the finest unit, is written whatever its value
		case field.DataType == timeseries.Float64, p.scales[i] == 1 && field.DataType != timeseries.Uint64:
			continue
		}
		scale, unsigned := p.scales[i], field.DataType == timeseries.Uint64
		for r := range rows {
			// a time that isn't a whole number of its unit, or a negative unsigned one, can't be written
			if ep := int64(seg.Epoch(r)); ep%scale != 0 || unsigned && ep < 0 {
				return timeseries.ErrInvalidTimeFormat
			}
		}
	}
	if mismatched {
		return timeseries.ErrInvalidBody
	}
	return nil
}

func (p *greptimePlan) checkValues() error {
	for _, result := range p.d.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			segs := series.Segments()
			for k := range segs {
				for c := range segs[k].NumCols() {
					if err := segs[k].CheckColumnJSON(c); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (p *greptimePlan) appendRow(b []byte, row greptimeRow) []byte {
	b = append(b, '[')
	for i := range p.fields {
		if i > 0 {
			b = append(b, ',')
		}
		switch field := &p.fields[i]; field.Role {
		case timeseries.RoleTimestamp:
			// the time was checked when the series was laid out
			b, _ = appendScaledEpoch(b, int64(row.epoch()), field, p.scales[i])
		case timeseries.RoleValue:
			b, _ = row.seg.AppendJSON(b, p.valueIndex[i], row.row)
		default:
			b = append(b, row.s.cells[i]...)
		}
	}
	return append(b, ']')
}

// rows yields the rows in the query's order: as stored without one, merged from the series as they're
// read when it starts with time, and otherwise sorted
func (p *greptimePlan) rows() iter.Seq[greptimeRow] {
	if len(p.order) == 0 {
		return p.stored
	}
	if ri, ok := p.timeOrdered(); ok {
		return func(yield func(greptimeRow) bool) {
			p.merged(ri, yield)
		}
	}
	rows := make([]greptimeRow, 0, p.d.ValueCount())
	for row := range p.stored {
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b greptimeRow) int {
		return p.compareTerms(a, b, p.order)
	})
	return slices.Values(rows)
}

func (p *greptimePlan) stored(yield func(greptimeRow) bool) {
	for ri, result := range p.d.Results {
		if result == nil {
			continue
		}
		for si, series := range result.SeriesList {
			if series == nil {
				continue
			}
			s, segs := p.layouts[ri][si], series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					if !yield(greptimeRow{s: s, seg: &segs[k], row: i}) {
						return
					}
				}
			}
		}
	}
}

// timeOrdered returns the one result a time-first order can merge in order, or false when there are
// others or a series isn't sorted
func (p *greptimePlan) timeOrdered() (int, bool) {
	if p.fields[p.order[0].index].Role != timeseries.RoleTimestamp {
		return 0, false
	}
	ri := -1
	for i, result := range p.d.Results {
		if result == nil {
			continue
		}
		if ri >= 0 {
			return 0, false
		}
		ri = i
		for _, series := range result.SeriesList {
			if series != nil && !series.IsSorted() {
				return 0, false
			}
		}
	}
	return ri, ri >= 0
}

func (p *greptimePlan) merged(ri int, yield func(greptimeRow) bool) {
	layouts := p.layouts[ri]
	order := dataset.RowOrder{Descending: p.order[0].Descending}
	// descending reads each series backward, so rows tied on every term need their stored order
	if rest := p.order[1:]; len(rest) > 0 || order.Descending {
		order.Compare = func(a, b dataset.Row) int {
			if c := p.compareTerms(greptimeRow{s: layouts[a.SeriesIndex], seg: a.Seg, row: a.Index},
				greptimeRow{s: layouts[b.SeriesIndex], seg: b.Seg, row: b.Index}, rest); c != 0 {
				return c
			}
			return dataset.CompareStored(a, b)
		}
	}
	for row := range p.d.Results[ri].Rows(order) {
		if !yield(greptimeRow{s: layouts[row.SeriesIndex], seg: row.Seg, row: row.Index}) {
			return
		}
	}
}

// compareTerms orders two rows by the terms: times as instants, then nulls where each term puts them,
// then values of a kind, read without boxing where it allows
func (p *greptimePlan) compareTerms(a, b greptimeRow, terms []orderColumn) int {
	for _, term := range terms {
		var c int
		switch vi := p.valueIndex[term.index]; {
		case p.fields[term.index].Role == timeseries.RoleTimestamp:
			// every row's time has the same field, so the times order as their epochs do
			c = cmp.Compare(a.epoch(), b.epoch())
		case vi >= 0:
			ka, kb := a.seg.KindAt(vi, a.row), b.seg.KindAt(vi, b.row)
			if ka == dataset.KindNull || kb == dataset.KindNull {
				if ka == kb {
					continue
				}
				return nullOrder(ka == dataset.KindNull, term.NullsFirst)
			}
			c = compareCells(a, b, vi, ka, kb)
		default:
			av, bv := a.s.tags[term.index], b.s.tags[term.index]
			if av == nil || bv == nil {
				if av == nil && bv == nil {
					continue
				}
				return nullOrder(av == nil, term.NullsFirst)
			}
			c = compareValue(av, bv)
		}
		if c != 0 {
			if term.Descending {
				return -c
			}
			return c
		}
	}
	return 0
}

func nullOrder(firstIsNull, nullsFirst bool) int {
	if firstIsNull == nullsFirst {
		return -1
	}
	return 1
}

// compareCells compares two values of value column c, as compareValue does
func compareCells(a, b greptimeRow, c int, ka, kb dataset.Kind) int {
	if ka == kb {
		switch ka {
		case dataset.KindInt64:
			return cmp.Compare(a.seg.Int64(c, a.row), b.seg.Int64(c, b.row))
		case dataset.KindUint64:
			return cmp.Compare(a.seg.Uint64(c, a.row), b.seg.Uint64(c, b.row))
		case dataset.KindFloat64:
			return cmp.Compare(a.seg.Float64(c, a.row), b.seg.Float64(c, b.row))
		case dataset.KindString:
			return cmp.Compare(a.seg.Text(c, a.row), b.seg.Text(c, b.row))
		case dataset.KindBool:
			return compareBools(a.seg.Bool(c, a.row), b.seg.Bool(c, b.row))
		}
	}
	return compareValue(a.seg.Value(c, a.row), b.seg.Value(c, b.row))
}

func compareBools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// compareValue orders two values of one type; others are equal. Number literals compare exactly.
func compareValue(a, b any) int {
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
			return cmp.Compare(av, bv)
		}
	case string:
		if bv, ok := b.(string); ok {
			return cmp.Compare(av, bv)
		}
	case bool:
		if bv, ok := b.(bool); ok {
			return compareBools(av, bv)
		}
	case json.Number:
		if bv, ok := b.(json.Number); ok {
			af, aok := new(big.Rat).SetString(string(av))
			bf, bok := new(big.Rat).SetString(string(bv))
			if aok && bok {
				return af.Cmp(bf)
			}
		}
	}
	return 0
}

// appends ep, of the field's scale, as epochValue writes it; a float time is exactly nine places, as
// big.Rat's FloatString writes it, since every scale divides a second
func appendScaledEpoch(b []byte, ep int64, field *timeseries.FieldDefinition, scale int64) ([]byte, error) {
	if scale == 0 {
		return b, timeseries.ErrInvalidTimeFormat
	}
	if field.DataType == timeseries.Float64 {
		if ep == math.MinInt64 {
			v, err := epochValue(ep, *field)
			if err != nil {
				return b, err
			}
			return append(b, v.(json.Number)...), nil
		}
		q, r := ep/scale, ep%scale
		if ep < 0 {
			b = append(b, '-')
			q, r = -q, -r
		}
		b = strconv.AppendInt(b, q, 10)
		b = append(b, '.')
		frac := r * (1e9 / scale)
		for d := int64(1e8); d > 1 && frac < d; d /= 10 {
			b = append(b, '0')
		}
		return strconv.AppendInt(b, frac, 10), nil
	}
	if ep%scale != 0 {
		return b, timeseries.ErrInvalidTimeFormat
	}
	value := ep / scale
	if field.DataType == timeseries.Uint64 {
		if value < 0 {
			return b, timeseries.ErrInvalidTimeFormat
		}
		return strconv.AppendUint(b, uint64(value), 10), nil
	}
	return strconv.AppendInt(b, value, 10), nil
}
