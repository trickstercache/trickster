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
	"math/big"
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
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
	r := &records{Schema: schema{Columns: make([]column, len(d.fields))}, Rows: make([][]any, 0)}
	for i, field := range d.fields {
		r.Schema.Columns[i] = column{Name: field.Name, Type: field.SDataType}
	}
	for _, result := range d.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			template := make([]any, len(d.fields))
			for i, field := range d.fields {
				if field.Role != timeseries.RoleTag {
					continue
				}
				encoded, ok := series.Header.Tags[field.Name]
				if !ok {
					return timeseries.ErrInvalidBody
				}
				decoder := json.NewDecoder(strings.NewReader(encoded))
				decoder.UseNumber()
				var value any
				if err := decoder.Decode(&value); err != nil {
					return err
				}
				v, err := decodeValue(value, field.SDataType)
				if err != nil {
					return err
				}
				template[i] = v
			}
			for _, point := range series.Points {
				row := slices.Clone(template)
				vi := 0
				for i, field := range d.fields {
					switch field.Role {
					case timeseries.RoleTimestamp:
						v, err := epochValue(int64(point.Epoch), field)
						if err != nil {
							return err
						}
						row[i] = v
					case timeseries.RoleValue:
						if vi >= len(point.Values) {
							return timeseries.ErrInvalidBody
						}
						row[i] = point.Values[vi]
						vi++
					}
				}
				if vi != len(point.Values) {
					return timeseries.ErrInvalidBody
				}
				r.Rows = append(r.Rows, row)
			}
		}
	}
	if d.TimeRangeQuery != nil {
		sortRows(r.Rows, d.fields, d.TimeRangeQuery.Ordering)
	}
	total, elapsed := uint64(len(r.Rows)), uint64(0)
	r.Total = &total
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set("Content-Type", "application/json")
		hw.Header().Set("X-Greptime-Format", "greptimedb_v1")
		hw.Header().Set("X-Greptime-Execution-Time", "0")
		hw.Header().Del("X-Greptime-Metrics")
	}
	return json.NewEncoder(w).Encode(response{Output: []output{{Records: r}}, ExecutionTime: &elapsed})
}

func sortRows(rows [][]any, fields timeseries.FieldDefinitions, ordering []timeseries.OrderTerm) {
	type orderColumn struct {
		timeseries.OrderTerm
		index int
	}
	columns := make([]orderColumn, 0, len(ordering))
	numbers := make(map[json.Number]*big.Rat)
	for _, term := range ordering {
		index := slices.IndexFunc(fields, func(field timeseries.FieldDefinition) bool { return field.Name == term.Column })
		if index < 0 {
			continue
		}
		columns = append(columns, orderColumn{term, index})
		for _, row := range rows {
			if n, ok := row[index].(json.Number); ok && numbers[n] == nil {
				numbers[n], _ = new(big.Rat).SetString(string(n))
			}
		}
	}
	slices.SortStableFunc(rows, func(a, b []any) int {
		for _, term := range columns {
			av, bv := a[term.index], b[term.index]
			if av == nil || bv == nil {
				if av == nil && bv == nil {
					continue
				}
				if (av == nil) == term.NullsFirst {
					return -1
				}
				return 1
			}
			comparison := compareValue(av, bv, numbers)
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
