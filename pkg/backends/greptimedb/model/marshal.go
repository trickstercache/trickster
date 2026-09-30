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
	"math"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

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
	rows, err := greptimeRows(d)
	if err != nil {
		return err
	}
	if d.TimeRangeQuery != nil {
		sortRows(rows, d.fields, d.TimeRangeQuery.Ordering)
	}
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set("Content-Type", "application/json")
		hw.Header().Set("X-Greptime-Format", "greptimedb_v1")
		hw.Header().Set("X-Greptime-Execution-Time", "0")
		hw.Header().Del("X-Greptime-Metrics")
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	for _, row := range rows {
		for j := range row.seg.NumCols() {
			if err := row.seg.CheckJSON(j, row.row); err != nil {
				return err
			}
		}
	}
	cw := tbytes.NewChunkWriter(w)
	cw.Buf = append(cw.Buf, `{"output":[{"records":{"schema":{"column_schemas":[`...)
	for i, field := range d.fields {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, `{"name":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, field.Name)
		cw.Buf = append(cw.Buf, `,"data_type":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, field.SDataType)
		cw.Buf = append(cw.Buf, '}')
	}
	cw.Buf = append(cw.Buf, `]},"rows":[`...)
	for r, row := range rows {
		if r > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, '[')
		vi := 0
		for i, field := range d.fields {
			if i > 0 {
				cw.Buf = append(cw.Buf, ',')
			}
			switch field.Role {
			case timeseries.RoleTimestamp:
				// the time was checked when the rows were built
				cw.Buf, _ = appendEpochValue(cw.Buf, int64(row.epoch()), field)
			case timeseries.RoleValue:
				cw.Buf, _ = row.seg.AppendJSON(cw.Buf, vi, row.row)
				vi++
			default:
				cw.Buf = append(cw.Buf, row.s.cells[i]...)
			}
		}
		cw.Buf = append(cw.Buf, ']')
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, `],"total_rows":`...)
	cw.Buf = strconv.AppendInt(cw.Buf, int64(len(rows)), 10)
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

// the row's value in column i, which is not its time
func (r greptimeRow) value(i int, fields timeseries.FieldDefinitions, valueIndex []int) any {
	if fields[i].Role == timeseries.RoleValue {
		return r.seg.Value(valueIndex[i], r.row)
	}
	return r.s.tags[i]
}

func greptimeRows(d *dataSet) ([]greptimeRow, error) {
	// the columns of the values and of the times, in order
	var valueFields, timeFields []int
	for i, field := range d.fields {
		switch field.Role {
		case timeseries.RoleValue:
			valueFields = append(valueFields, i)
		case timeseries.RoleTimestamp:
			timeFields = append(timeFields, i)
		}
	}
	count := 0
	for _, result := range d.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series != nil {
				count += series.PointCount()
			}
		}
	}
	rows := make([]greptimeRow, 0, count)
	for _, result := range d.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			s := &greptimeSeries{tags: make([]any, len(d.fields)), cells: make([][]byte, len(d.fields))}
			for i, field := range d.fields {
				if field.Role != timeseries.RoleTag {
					s.cells[i] = []byte("null")
					continue
				}
				encoded, ok := series.Header.Tags[field.Name]
				if !ok {
					return nil, timeseries.ErrInvalidBody
				}
				decoder := json.NewDecoder(strings.NewReader(encoded))
				decoder.UseNumber()
				var value any
				if err := decoder.Decode(&value); err != nil {
					return nil, err
				}
				v, err := decodeValue(value, field.SDataType)
				if err != nil {
					return nil, err
				}
				if s.cells[i], err = tstrings.AppendJSONValue(nil, v); err != nil {
					return nil, err
				}
				s.tags[i] = v
			}
			segs := series.Segments()
			for k := range segs {
				seg := &segs[k]
				for i := range seg.Len() {
					if err := checkRow(seg, i, d.fields, timeFields, valueFields); err != nil {
						return nil, err
					}
					rows = append(rows, greptimeRow{s: s, seg: seg, row: i})
				}
			}
		}
	}
	return rows, nil
}

// fails as building the row field by field did: at the first bad time, or the value field past the
// last value, whichever is first, or after all fields when values are left over
func checkRow(seg *dataset.Segment, row int, fields timeseries.FieldDefinitions, timeFields, valueFields []int) error {
	stop := len(fields)
	if n := seg.NumCols(); n < len(valueFields) {
		stop = valueFields[n]
	}
	for _, i := range timeFields {
		if i >= stop {
			break
		}
		if err := checkEpochValue(int64(seg.Epoch(row)), fields[i]); err != nil {
			return err
		}
	}
	if seg.NumCols() != len(valueFields) {
		return timeseries.ErrInvalidBody
	}
	return nil
}

// the error epochValue returns for ep, found without formatting it
func checkEpochValue(ep int64, field timeseries.FieldDefinition) error {
	scale := axisScale(field)
	switch {
	case scale == 0:
		return timeseries.ErrInvalidTimeFormat
	case field.DataType == timeseries.Float64:
		return nil
	case ep%scale != 0, field.DataType == timeseries.Uint64 && ep < 0:
		return timeseries.ErrInvalidTimeFormat
	}
	return nil
}

// appends epochValue's value for ep as JSON; a float time is written to nine places, exactly, as
// big.Rat's FloatString writes it, since every scale divides a second in nanoseconds
func appendEpochValue(b []byte, ep int64, field timeseries.FieldDefinition) ([]byte, error) {
	scale := axisScale(field)
	if scale == 0 {
		return b, timeseries.ErrInvalidTimeFormat
	}
	if field.DataType == timeseries.Float64 {
		if ep == math.MinInt64 {
			v, err := epochValue(ep, field)
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

func sortRows(rows []greptimeRow, fields timeseries.FieldDefinitions, ordering []timeseries.OrderTerm) {
	type orderColumn struct {
		timeseries.OrderTerm
		index int
	}
	valueIndex := make([]int, len(fields))
	vi := 0
	for i, field := range fields {
		if field.Role == timeseries.RoleValue {
			valueIndex[i] = vi
			vi++
		}
	}
	columns := make([]orderColumn, 0, len(ordering))
	numbers := make(map[json.Number]*big.Rat)
	for _, term := range ordering {
		index := slices.IndexFunc(fields, func(field timeseries.FieldDefinition) bool { return field.Name == term.Column })
		if index < 0 {
			continue
		}
		columns = append(columns, orderColumn{term, index})
		if fields[index].Role == timeseries.RoleTimestamp {
			continue
		}
		for _, row := range rows {
			if n, ok := row.value(index, fields, valueIndex).(json.Number); ok && numbers[n] == nil {
				numbers[n], _ = new(big.Rat).SetString(string(n))
			}
		}
	}
	if len(columns) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b greptimeRow) int {
		for _, term := range columns {
			var comparison int
			if fields[term.index].Role == timeseries.RoleTimestamp {
				// every row's time has the same field, so the times order as their epochs do
				comparison = cmp.Compare(a.epoch(), b.epoch())
			} else {
				av, bv := a.value(term.index, fields, valueIndex), b.value(term.index, fields, valueIndex)
				if av == nil || bv == nil {
					if av == nil && bv == nil {
						continue
					}
					if (av == nil) == term.NullsFirst {
						return -1
					}
					return 1
				}
				comparison = compareValue(av, bv, numbers)
			}
			if comparison != 0 {
				if term.Descending {
					return -comparison
				}
				return comparison
			}
		}
		return 0
	})
}

func compareValue(a, b any, numbers map[json.Number]*big.Rat) int {
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
			if av == bv {
				return 0
			}
			if av {
				return 1
			}
			return -1
		}
	case json.Number:
		if bv, ok := b.(json.Number); ok {
			af, bf := numbers[av], numbers[bv]
			if af != nil && bf != nil {
				return af.Cmp(bf)
			}
		}
	}
	return 0
}
