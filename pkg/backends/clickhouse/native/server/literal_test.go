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

package server

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func TestParseTextLiteral(t *testing.T) {
	for _, c := range []struct {
		in   string
		want any
	}{
		{"[1,2]", []any{"1", "2"}},
		{"[]", []any{}},
		{"['a','b\\'c','x\\\\y','t\\tab']", []any{"a", "b'c", "x\\y", "t\tab"}},
		{"[[1],[2,3]]", []any{[]any{"1"}, []any{"2", "3"}}},
		{"('x',2,NULL)", []any{"x", "2", nil}},
		{"{'a':1,'b':2}", map[string]any{"a": "1", "b": "2"}},
		{"{1:['x']}", map[string]any{"1": []any{"x"}}},
		{"NULL", nil},
		{"42", "42"},
		{"true", "true"},
		{" [ 1 , 2 ] ", []any{"1", "2"}},
	} {
		got, err := ParseTextLiteral(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %#v, want %#v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"[1,2", "{'a'}", "{'a':", "'open", "[1]]", "['a\\", "[,]", ""} {
		if _, err := ParseTextLiteral(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestEncodeCompoundFromTextLiterals(t *testing.T) {
	for _, c := range []struct {
		typ, text string
		want      any
	}{
		{"Array(UInt8)", "[1,2]", []uint8{1, 2}},
		{"Array(String)", "['a','b\\'c']", []string{"a", "b'c"}},
		{"Array(Array(UInt16))", "[[1],[2,3]]", [][]uint16{{1}, {2, 3}}},
		{"Array(Nullable(UInt8))", "[1,NULL]", []*uint8{new(uint8(1)), nil}},
		{"Map(String, UInt64)", "{'a':1,'b':2}", map[string]uint64{"a": 1, "b": 2}},
		{"Map(UInt8, String)", "{7:'x'}", map[uint8]string{7: "x"}},
		{"Tuple(String, UInt8)", "('x',2)", []any{"x", uint8(2)}},
		{"Array(Bool)", "[true,false]", []bool{true, false}},
		{"Array(DateTime)", "['2020-01-01 00:00:00']", []time.Time{time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}},
	} {
		t.Run(c.typ, func(t *testing.T) {
			var out bytes.Buffer
			if err := encodeColumnRevision(&out, c.typ, []any{c.text}, ServerRevision); err != nil {
				t.Fatal(err)
			}
			col, err := column.Type(c.typ).Column("x", &column.ServerContext{Revision: ServerRevision, Timezone: time.UTC})
			if err != nil {
				t.Fatal(err)
			}
			if err := col.Decode(proto.NewReader(&out), 1); err != nil {
				t.Fatal(err)
			}
			if got := col.Row(0, false); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
	var out bytes.Buffer
	if err := encodeColumnRevision(&out, "Array(UInt8)", []any{"[1,"}, ServerRevision); err == nil {
		t.Fatal("expected a parse error for a malformed literal")
	}
}

//go:fix inline
func ptr[T any](v T) *T { return new(v) }
