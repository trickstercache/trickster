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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func query() *timeseries.TimeRangeQuery {
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "fixture", OutputColumn: "time", GroupColumns: []string{"host"},
		ValueColumns: []string{"value"}, Step: time.Second, OutputUnit: timeseries.DateTimeRFC3339Nano,
		Ordering: []timeseries.OrderTerm{{Column: "time", Descending: true}, {Column: "host", NullsFirst: true}},
	}
	trq := sqlanalyzer.NewTimeRangeQuery("fixture")
	plan.ApplyToQuery(trq)
	trq.Extent = timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(10, 0)}
	return trq
}

func envelope(rows string, total int) string {
	return fmt.Sprintf(`{"output":[{"records":{"schema":{"column_schemas":[{"name":"value","data_type":"UInt64"},{"name":"time","data_type":"TimestampNanosecond"},{"name":"host","data_type":"String"}]},"rows":%s,"total_rows":%d}}],"execution_time_ms":5}`, rows, total)
}

func decoded(t testing.TB, body []byte) response {
	t.Helper()
	var out response
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestModelCacheRoundTrip(t *testing.T) {
	input := envelope(`[[18446744073709551615,1000000000,"a"],[9007199254740993,2000000000,"null"],[null,2000000000,null],[7,2000000000,""]]`, 4)
	trq := query()
	ts, err := UnmarshalTimeseries([]byte(input), trq)
	if err != nil {
		t.Fatal(err)
	}
	if ts.SeriesCount() != 4 {
		t.Fatalf("groups collapsed: %d", ts.SeriesCount())
	}
	for _, cacheRoundTrip := range []bool{false, true} {
		t.Run(fmt.Sprint(cacheRoundTrip), func(t *testing.T) {
			candidate := ts.Clone()
			if cacheRoundTrip {
				buf, err := marshalCache(candidate, nil, 200)
				if err != nil {
					t.Fatal(err)
				}
				candidate, err = unmarshalCache(buf, trq)
				if err != nil {
					t.Fatal(err)
				}
			}
			body, err := MarshalTimeseries(candidate, nil, 200)
			if err != nil {
				t.Fatal(err)
			}
			out := decoded(t, body)
			want := decoded(t, []byte(envelope(`[[null,2000000000,null],[7,2000000000,""],[9007199254740993,2000000000,"null"],[18446744073709551615,1000000000,"a"]]`, 4)))
			if !reflect.DeepEqual(out.Output, want.Output) || out.ExecutionTime == nil || *out.ExecutionTime != 0 {
				t.Fatalf("fidelity lost: %s", body)
			}
		})
	}
}

func TestSchemaSuppliesUnnamedValueColumns(t *testing.T) {
	trq := query()
	trq.ParsedQuery.(*sqlanalyzer.QueryPlan).ValueColumns = nil
	if _, err := UnmarshalTimeseries([]byte(envelope(`[[7,1000000000,"a"]]`, 1)), trq); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(envelope(`[]`, 0), `"name":"host"`, `"name":"other"`, 1),
		strings.Replace(envelope(`[]`, 0), `"name":"value"`, `"name":"host"`, 1),
	} {
		if _, err := UnmarshalTimeseries([]byte(bad), trq); err == nil {
			t.Fatal("missing or duplicate grouping column accepted")
		}
	}
}

func TestModelEmptyAndCroppedSchema(t *testing.T) {
	for _, rows := range []string{`[]`, `[[7,1000000000,"a"]]`} {
		t.Run(rows, func(t *testing.T) {
			total := 0
			if rows != "[]" {
				total = 1
			}
			trq := query()
			ts, err := UnmarshalTimeseries([]byte(envelope(rows, total)), trq)
			if err != nil {
				t.Fatal(err)
			}
			cropped := ts.CroppedClone(timeseries.Extent{Start: time.Unix(3, 0), End: time.Unix(4, 0)})
			buf, err := marshalCache(cropped, nil, 200)
			if err != nil {
				t.Fatal(err)
			}
			cropped, err = unmarshalCache(buf, trq)
			if err != nil {
				t.Fatal(err)
			}
			body, err := MarshalTimeseries(cropped, nil, 200)
			if err != nil {
				t.Fatal(err)
			}
			got := decoded(t, body).Output[0].Records
			if len(got.Schema.Columns) != 3 || got.Rows == nil || len(got.Rows) != 0 || *got.Total != 0 {
				t.Fatalf("empty schema lost: %s", body)
			}
			if ts.ValueCount() != int64(total) {
				t.Fatal("cropped clone mutated source")
			}
		})
	}
}

func TestModelMerge(t *testing.T) {
	trq := query()
	a, err := UnmarshalTimeseries([]byte(envelope(`[]`, 0)), trq)
	if err != nil {
		t.Fatal(err)
	}
	b, err := UnmarshalTimeseries([]byte(envelope(`[[2,2000000000,"a"],[1,1000000000,"a"]]`, 2)), trq)
	if err != nil {
		t.Fatal(err)
	}
	a.Merge(true, b)
	if a.ValueCount() != 2 {
		t.Fatal("empty extent could not merge data")
	}
	bad := strings.Replace(envelope(`[[3,3000000000,"a"]]`, 1), "UInt64", "Int64", 1)
	c, err := UnmarshalTimeseries([]byte(bad), trq)
	if err != nil {
		t.Fatal(err)
	}
	a.Merge(true, c)
	if _, err := MarshalTimeseries(a, nil, 200); err == nil {
		t.Fatal("incompatible schemas silently merged")
	}
	if _, err := marshalCache(a, nil, 200); err == nil {
		t.Fatal("incompatible schema stored")
	}
}

func TestModelMalformedResponses(t *testing.T) {
	base := envelope(`[[7,1000000000,"a"]]`, 1)
	for name, input := range map[string]string{
		"empty": "", "trailing": base + "{}", "error": `{"code":1004,"error":"failed","execution_time_ms":0}`,
		"affected_rows":     `{"output":[{"affectedrows":1}],"execution_time_ms":0}`,
		"multi":             strings.Replace(base, `],"execution_time_ms"`, ` ,{"records":{}}],"execution_time_ms"`, 1),
		"extra_field":       strings.Replace(base, `"total_rows":1`, `"total_rows":1,"extra":true`, 1),
		"metrics":           strings.Replace(base, `"total_rows":1`, `"total_rows":1,"metrics":{"elapsed":1}`, 1),
		"truncated":         strings.Replace(base, `"total_rows":1`, `"total_rows":2`, 1),
		"missing_total":     strings.Replace(base, `,"total_rows":1`, "", 1),
		"null_rows":         envelope("null", 0),
		"short_row":         envelope(`[[7,1000000000]]`, 1),
		"long_row":          envelope(`[[7,1000000000,"a",1]]`, 1),
		"duplicate_columns": strings.Replace(base, `"name":"host"`, `"name":"value"`, 1),
		"wrong_column":      strings.Replace(base, `"name":"host"`, `"name":"other"`, 1),
		"unknown_type":      strings.Replace(base, "UInt64", "Decimal128(38, 2)", 1),
		"null_time":         envelope(`[[7,null,"a"]]`, 1),
		"fraction_time":     envelope(`[[7,1.5,"a"]]`, 1),
		"off_grid":          envelope(`[[7,1000000001,"a"]]`, 1),
		"overflow_time":     envelope(`[[7,9223372036854775808,"a"]]`, 1),
		"overflow_value":    envelope(`[[18446744073709551616,1000000000,"a"]]`, 1),
		"negative_unsigned": envelope(`[[-1,1000000000,"a"]]`, 1),
		"wrong_tag_type":    envelope(`[[7,1000000000,1]]`, 1),
		"duplicate_point":   envelope(`[[7,1000000000,"a"],[8,1000000000,"a"]]`, 2),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalTimeseries([]byte(input), query()); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestTypedValues(t *testing.T) {
	for _, test := range []struct {
		typ, value string
		valid      bool
	}{
		{"Int8", "-128", true},
		{"Int8", "128", false},
		{"UInt8", "255", true},
		{"UInt8", "256", false},
		{"Int16", "-32768", true},
		{"Int32", "2147483647", true},
		{"Int64", "-9223372036854775808", true},
		{"UInt16", "65535", true},
		{"UInt32", "4294967295", true},
		{"UInt64", "18446744073709551615", true},
		{"Float32", "0.1", true},
		{"Float32", "3.5e38", false},
		{"Float64", "1.25e-20", true},
		{"Float64", "1e1000", false},
		{"String", `"a\u0000b"`, true},
		{"Boolean", "true", true},
		{"Boolean", "1", false},
		{"Null", "null", true},
		{"Null", "1", false},
		{"TimestampSecond", "-1", true},
		{"Date", "12345", true},
	} {
		t.Run(test.typ+test.value, func(t *testing.T) {
			d := json.NewDecoder(strings.NewReader(test.value))
			d.UseNumber()
			var input any
			if err := d.Decode(&input); err != nil {
				t.Fatal(err)
			}
			v, err := decodeValue(input, test.typ)
			if (err == nil) != test.valid {
				t.Fatalf("value=%v err=%v", v, err)
			}
		})
	}
}

func TestModelRejectsValuelessSeries(t *testing.T) {
	trq := query()
	trq.ParsedQuery.(*sqlanalyzer.QueryPlan).ValueColumns = nil
	input := `{"output":[{"records":{"schema":{"column_schemas":[{"name":"time","data_type":"TimestampNanosecond"},{"name":"host","data_type":"String"}]},"rows":[[1000000000,"a"]],"total_rows":1}}],"execution_time_ms":0}`
	if _, err := UnmarshalTimeseries([]byte(input), trq); err == nil {
		t.Fatal("accepted a result whose rows would be discarded during cropping")
	}
}

func TestModelAmbiguousFloatOrdering(t *testing.T) {
	input := strings.Replace(envelope(`[[null,1000000000,"a"],[1,2000000000,"a"]]`, 2), "UInt64", "Float64", 1)
	trq := query()
	if _, err := UnmarshalTimeseries([]byte(input), trq); err != nil {
		t.Fatal("nullable float not used for sorting should be preserved", err)
	}
	plan := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	plan.Ordering = append(plan.Ordering, timeseries.OrderTerm{Column: "value"})
	if _, err := UnmarshalTimeseries([]byte(input), trq); err == nil {
		t.Fatal("cannot distinguish NULL from non-finite floats when rebuilding sort order")
	}
}

func TestModelRequiresOrderingColumns(t *testing.T) {
	trq := query()
	plan := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	plan.ValueColumns = nil
	plan.Ordering = []timeseries.OrderTerm{{Column: "missing_alias"}}
	if _, err := UnmarshalTimeseries([]byte(envelope(`[[7,1000000000,"a"]]`, 1)), trq); err == nil {
		t.Fatal("accepted a schema that cannot preserve the requested ordering")
	}
}

func TestExactNumericSort(t *testing.T) {
	rows := [][]any{{json.Number("9007199254740993")}, {json.Number("9.007199254740992e15")}, {nil}}
	fields := timeseries.FieldDefinitions{{Name: "time"}}
	sortRows(rows, fields, []timeseries.OrderTerm{{Column: "time", NullsFirst: true}})
	if rows[0][0] != nil || rows[1][0] != json.Number("9.007199254740992e15") || rows[2][0] != json.Number("9007199254740993") {
		t.Fatal("sort rounded distinct numbers", rows)
	}
}

func TestModelValueOrdering(t *testing.T) {
	for _, test := range []struct {
		typ, low, high string
	}{
		{"Int64", "-9223372036854775808", "9223372036854775807"},
		{"UInt64", "9007199254740992", "9007199254740993"},
		{"Float64", "-1.25", "0.5"},
		{"String", `"a"`, `"b"`},
		{"Boolean", "false", "true"},
	} {
		for _, descending := range []bool{false, true} {
			for _, nullsFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/desc=%t/nullsFirst=%t", test.typ, descending, nullsFirst), func(t *testing.T) {
					rows := fmt.Sprintf(`[[%s,1000000000,"a"],[%s,2000000000,"a"]`, test.high, test.low)
					total := 2
					if test.typ != "Float64" {
						rows += `,[null,3000000000,"a"]`
						total++
					}
					input := strings.Replace(envelope(rows+"]", total), "UInt64", test.typ, 1)
					trq := query()
					plan := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
					plan.Ordering = []timeseries.OrderTerm{{Column: "value", Descending: descending, NullsFirst: nullsFirst}}
					plan.ApplyToQuery(trq)
					ts, err := UnmarshalTimeseries([]byte(input), trq)
					if err != nil {
						t.Fatal(err)
					}
					want := []json.Number{"2000000000", "1000000000"}
					if descending {
						want[0], want[1] = want[1], want[0]
					}
					if total == 3 {
						if nullsFirst {
							want = append([]json.Number{"3000000000"}, want...)
						} else {
							want = append(want, "3000000000")
						}
					}
					for range 2 {
						wire, err := MarshalTimeseries(ts, nil, 200)
						if err != nil {
							t.Fatal(err)
						}
						got := decoded(t, wire).Output[0].Records.Rows
						if len(got) != len(want) {
							t.Fatalf("row count changed: %s", wire)
						}
						for i, row := range got {
							if row[1] != want[i] {
								t.Fatalf("row order changed: %s", wire)
							}
						}
						cached, err := marshalCache(ts, nil, 200)
						if err != nil {
							t.Fatal(err)
						}
						ts, err = unmarshalCache(cached, trq)
						if err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestTimestampUnitsAndPrecision(t *testing.T) {
	for _, test := range []struct {
		typ, value string
		unit       timeseries.FieldDataType
		want       int64
		valid      bool
	}{
		{"TimestampSecond", "-1", 0, -1e9, true},
		{"TimestampMillisecond", "-1", 0, -1e6, true},
		{"TimestampMicrosecond", "-1", 0, -1e3, true},
		{"TimestampNanosecond", "1704067200123456789", 0, 1704067200123456789, true},
		{"TimestampSecond", "9223372037", 0, 0, false},
		{"TimestampSecond", "-9223372037", 0, 0, false},
		{"Int64", "1704067200", timeseries.DateTimeUnixSecs, 1704067200000000000, true},
		{"Float64", "1704067200.123456789", timeseries.DateTimeUnixSecs, 1704067200123456789, true},
		{"Float64", "0.0000000001", timeseries.DateTimeUnixSecs, 0, false},
		{"Float64", "1e3", timeseries.DateTimeUnixMilli, 1e9, true},
		{"String", "0", 0, 0, false},
	} {
		t.Run(test.typ+test.value, func(t *testing.T) {
			typ, _ := fieldType(test.typ)
			field := timeseries.FieldDefinition{DataType: typ, SDataType: test.typ, ProviderData1: byte(test.unit)}
			got, err := parseEpoch(json.Number(test.value), field)
			if (err == nil) != test.valid || (test.valid && got != test.want) {
				t.Fatalf("epoch=%d err=%v", got, err)
			}
			if test.valid {
				v, err := epochValue(got, field)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				back, err := parseEpoch(json.Number(raw), field)
				if err != nil || back != got {
					t.Fatalf("axis changed: %s %v", raw, err)
				}
			}
		})
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestModelGuardsAndWriter(t *testing.T) {
	trq := query()
	ts, err := UnmarshalTimeseries([]byte(envelope(`[]`, 0)), trq)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := MarshalTimeseriesWriter(ts, nil, 200, w); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Greptime-Execution-Time") != "0" {
		t.Fatal("missing response metadata")
	}
	if err := MarshalTimeseriesWriter(ts, nil, 200, brokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := UnmarshalTimeseriesReader(nil, trq); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := UnmarshalTimeseries(nil, nil); err == nil {
		t.Fatal("nil query accepted")
	}
	if _, err := UnmarshalTimeseries([]byte(envelope(`[]`, 0)), &timeseries.TimeRangeQuery{}); err == nil {
		t.Fatal("missing plan accepted")
	}
	if _, err := MarshalTimeseries(&dataset.DataSet{}, nil, 200); err == nil {
		t.Fatal("unknown model accepted")
	}
	if _, err := marshalCache(nil, nil, 200); err == nil {
		t.Fatal("nil cache object accepted")
	}
	buf, err := marshalCache(ts, nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("GSQL"), append(bytes.Clone(buf), 0), buf[:len(buf)-1]} {
		if _, err := unmarshalCache(bad, trq); err == nil {
			t.Fatal("invalid cache accepted")
		}
	}
}

func BenchmarkModelRoundTrip(b *testing.B) {
	input := []byte(envelope(`[[18446744073709551615,1000000000,"a"],[9007199254740993,2000000000,"b"]]`, 2))
	trq := query()
	for b.Loop() {
		ts, err := UnmarshalTimeseries(input, trq)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := MarshalTimeseries(ts, nil, 200); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzModelDecode(f *testing.F) {
	f.Add([]byte(envelope(`[[7,1000000000,"a"]]`, 1)))
	f.Add([]byte(envelope(`[]`, 0)))
	f.Fuzz(func(t *testing.T, body []byte) {
		ts, err := UnmarshalTimeseries(body, query())
		if err != nil {
			return
		}
		if _, err := MarshalTimeseries(ts, nil, 200); err != nil {
			t.Fatal(err)
		}
	})
}
