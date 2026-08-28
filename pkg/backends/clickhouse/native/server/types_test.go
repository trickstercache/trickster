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

package server

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func TestColumnCodecRoundTrip(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		typ         string
		input, want any
	}{
		{"UInt64", json.Number("18446744073709551615"), uint64(18446744073709551615)},
		{"Int64", "-9223372036854775808", int64(-9223372036854775808)},
		{"Float64", json.Number("1.25"), float64(1.25)},
		{"Bool", true, true},
		{"String", "a\tb\n", "a\tb\n"},
		{"Nullable(UInt32)", nil, nil},
		{"Array(UInt32)", []any{json.Number("1"), json.Number("2")}, []uint32{1, 2}},
		{"Map(String, UInt32)", map[string]any{"a": json.Number("1")}, map[string]uint32{"a": 1}},
		{"LowCardinality(String)", "a", "a"},
		{"DateTime64(6)", "2020-01-01 00:00:00.123456", time.Unix(1577836800, 123456000).UTC()},
		{"DateTime('America/Denver')", "2020-01-01 00:00:00", time.Date(2020, 1, 1, 0, 0, 0, 0, denver)},
	}
	for _, test := range tests {
		t.Run(test.typ, func(t *testing.T) {
			var out bytes.Buffer
			if err := encodeColumnRevision(&out, test.typ, []any{test.input}, ServerRevision); err != nil {
				t.Fatal(err)
			}
			col, err := column.Type(test.typ).Column("x", &column.ServerContext{Revision: ServerRevision, Timezone: time.UTC})
			if err != nil {
				t.Fatal(err)
			}
			r := proto.NewReader(&out)
			if custom, ok := col.(column.CustomSerialization); ok {
				if err := custom.ReadStatePrefix(r); err != nil {
					t.Fatal(err)
				}
			}
			if err := col.Decode(r, 1); err != nil {
				t.Fatal(err)
			}
			if got := col.Row(0, false); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		typ   string
		value any
	}{{"UInt8", "256"}, {"DateTime64(6)", "invalid"}, {"Unknown", "x"}, {"FixedString(4)", "abcdef"}} {
		var out bytes.Buffer
		if err := encodeColumnRevision(&out, test.typ, []any{test.value}, ServerRevision); err == nil {
			t.Fatalf("accepted invalid %s value %v", test.typ, test.value)
		}
	}
}

func TestExtendedColumnCodecs(t *testing.T) {
	for _, test := range []struct {
		typ   string
		value any
	}{
		{"UInt128", "340282366920938463463374607431768211455"},
		{"Int128", "-170141183460469231731687303715884105728"},
		{"Decimal(38, 9)", "12345678901234567890123456789.123456789"},
		{"Array(Tuple(String, Nullable(UInt64)))", []any{[]any{"a", json.Number("18446744073709551615")}, []any{"b", nil}}},
		{"Tuple(String, Array(DateTime64(3, 'America/Denver')))", []any{"times", []any{"2020-01-01 00:00:00.123"}}},
	} {
		t.Run(test.typ, func(t *testing.T) {
			var out bytes.Buffer
			if err := encodeColumnRevision(&out, test.typ, []any{test.value}, ServerRevision); err != nil {
				col, _ := column.Type(test.typ).Column("x", &column.ServerContext{Revision: ServerRevision, Timezone: time.UTC})
				t.Fatalf("scan type %s: %v", col.ScanType(), err)
			}
			col, err := column.Type(test.typ).Column("x", &column.ServerContext{Revision: ServerRevision, Timezone: time.UTC})
			if err != nil {
				t.Fatal(err)
			}
			reader := proto.NewReader(&out)
			if custom, ok := col.(column.CustomSerialization); ok {
				if err := custom.ReadStatePrefix(reader); err != nil {
					t.Fatal(err)
				}
			}
			if err := col.Decode(reader, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}
