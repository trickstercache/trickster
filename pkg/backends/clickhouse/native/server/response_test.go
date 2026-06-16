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
	"encoding/binary"
	"testing"
)

func TestZeroForType(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"UInt8", uint8(0)},
		{"Int8", uint8(0)},
		{"UInt16", uint16(0)},
		{"Int16", uint16(0)},
		{"UInt32", uint32(0)},
		{"Int32", uint32(0)},
		{"DateTime", uint32(0)},
		{"UInt64", uint64(0)},
		{"Int64", uint64(0)},
		{"DateTime64", uint64(0)},
		{"Float32", float32(0)},
		{"Float64", float64(0)},
		{"String", ""},
		{"UUID", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := zeroForType(c.in); got != c.want {
				t.Fatalf("zeroForType(%q): want %v (%T), got %v (%T)", c.in, c.want, c.want, got, got)
			}
		})
	}
}

func TestToMap(t *testing.T) {
	m := map[string]any{"a": 1, "b": "two"}
	if got := toMap(m); len(got) != 2 || got["a"] != 1 || got["b"] != "two" {
		t.Fatalf("map passthrough failed: %v", got)
	}
	if got := toMap("not a map"); got != nil {
		t.Fatalf("non-map should be nil, got %v", got)
	}
	if got := toMap(nil); got != nil {
		t.Fatalf("nil should be nil, got %v", got)
	}
}

func TestSplitMapTypes(t *testing.T) {
	cases := []struct {
		in           string
		wantK, wantV string
	}{
		{"String, UInt32", "String", "UInt32"},
		{"String,Array(UInt32)", "String", "Array(UInt32)"},
		{"Tuple(Int32, Int32), String", "Tuple(Int32, Int32)", "String"},
		{"OnlyOne", "OnlyOne", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			k, v := splitMapTypes(c.in)
			if k != c.wantK || v != c.wantV {
				t.Fatalf("splitMapTypes(%q): want (%q,%q), got (%q,%q)", c.in, c.wantK, c.wantV, k, v)
			}
		})
	}
}

func TestWriteLowCardinality(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{"a", "b", "a", "c", "a"}
	if err := writeLowCardinality(w, "String", values); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	ver, _ := readInt64(r, t)
	if ver != 1 {
		t.Fatalf("serialization version: want 1, got %d", ver)
	}
	flags, _ := readInt64(r, t)
	if flags&0x7 != 0 { // <=256 dict -> index type 0 (byte)
		t.Fatalf("flags low bits should be 0, got %d", flags&0x7)
	}
	if flags&(1<<9) == 0 {
		t.Fatal("need_update_dictionary flag should be set")
	}

	dictLen, _ := readInt64(r, t)
	if dictLen != 3 {
		t.Fatalf("dict len: want 3, got %d", dictLen)
	}
	s0, _ := r.str()
	s1, _ := r.str()
	s2, _ := r.str()
	if s0 != "a" || s1 != "b" || s2 != "c" {
		t.Fatalf("dict values: got %q %q %q", s0, s1, s2)
	}

	indicesLen, _ := readInt64(r, t)
	if indicesLen != int64(len(values)) {
		t.Fatalf("indices len: want %d, got %d", len(values), indicesLen)
	}
	for i, want := range []byte{0, 1, 0, 2, 0} {
		got, err := r.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("index[%d]: want %d, got %d", i, want, got)
		}
	}
}

func TestWriteLowCardinalityUInt16Index(t *testing.T) {
	// >256 unique values triggers uint16 index encoding.
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := make([]any, 0, 300)
	for i := 0; i < 300; i++ {
		values = append(values, int32(i))
	}
	if err := writeLowCardinality(w, "Int32", values); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	_, _ = readInt64(r, t)       // version
	flags, _ := readInt64(r, t)
	if flags&0x7 != 1 {
		t.Fatalf("expected index type 1 (uint16), got %d", flags&0x7)
	}
	dictLen, _ := readInt64(r, t)
	if dictLen != 300 {
		t.Fatalf("dict len: want 300, got %d", dictLen)
	}
	// skip 300 Int32 dict entries
	for i := 0; i < 300; i++ {
		if _, err := r.int32(); err != nil {
			t.Fatal(err)
		}
	}
	indicesLen, _ := readInt64(r, t)
	if indicesLen != 300 {
		t.Fatalf("indices len: want 300, got %d", indicesLen)
	}
	// first index should be 0
	var b [2]byte
	r.Read(b[:])
	if binary.LittleEndian.Uint16(b[:]) != 0 {
		t.Fatalf("first index: want 0, got %d", binary.LittleEndian.Uint16(b[:]))
	}
}

func TestWriteColumnDataMap(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{
		map[string]any{"k1": uint32(1)},
		map[string]any{"k2": uint32(2), "k3": uint32(3)},
	}
	if err := writeColumnData(w, "Map(String, UInt32)", values); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	// offsets: 1, 3
	var off [8]byte
	r.Read(off[:])
	if binary.LittleEndian.Uint64(off[:]) != 1 {
		t.Fatalf("first offset: want 1, got %d", binary.LittleEndian.Uint64(off[:]))
	}
	r.Read(off[:])
	if binary.LittleEndian.Uint64(off[:]) != 3 {
		t.Fatalf("second offset: want 3, got %d", binary.LittleEndian.Uint64(off[:]))
	}
	// keys: 3 strings (order within a map is not deterministic, so just count)
	for i := 0; i < 3; i++ {
		if _, err := r.str(); err != nil {
			t.Fatalf("reading key %d: %v", i, err)
		}
	}
	// values: 3 UInt32
	for i := 0; i < 3; i++ {
		if _, err := r.int32(); err != nil {
			t.Fatalf("reading value %d: %v", i, err)
		}
	}
}

func TestWriteColumnDataLowCardinality(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeColumnData(w, "LowCardinality(String)", []any{"x", "y", "x"}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestWriteBlockContentNoRows(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	cols := []Column{{Name: "x", Type: "UInt32"}}
	if err := writeBlockContent(w, cols, nil, 0); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
}

// readInt64 is a tiny helper for tests reading back putInt64-encoded values.
func readInt64(r *protoReader, t *testing.T) (int64, error) {
	t.Helper()
	var b [8]byte
	if _, err := r.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil //nolint:gosec
}
