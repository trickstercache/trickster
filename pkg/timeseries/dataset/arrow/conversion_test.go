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

package arrow

import (
	"errors"
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// TestNumericConverterTolerance covers the alternate value representations
// the cache serialization layer may substitute, and their range/sign limits.
func TestNumericConverterTolerance(t *testing.T) {
	t.Run("asInt64", func(t *testing.T) {
		for _, tc := range []struct {
			in      any
			want    int64
			wantErr bool
		}{
			{int64(-5), -5, false},
			{uint64(7), 7, false},
			{int(9), 9, false},
			{float64(3), 3, false},
			{uint64(math.MaxUint64), 0, true},
			{"nope", 0, true},
			{true, 0, true},
		} {
			got, err := asInt64(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("asInt64(%v) = %d, %v", tc.in, got, err)
			}
		}
	})

	t.Run("asUint64", func(t *testing.T) {
		for _, tc := range []struct {
			in      any
			want    uint64
			wantErr bool
		}{
			{uint64(math.MaxUint64), math.MaxUint64, false},
			{int64(7), 7, false},
			{int(9), 9, false},
			{float64(3), 3, false},
			{int64(-1), 0, true},
			{int(-1), 0, true},
			{float64(-1), 0, true},
			{"nope", 0, true},
		} {
			got, err := asUint64(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("asUint64(%v) = %d, %v", tc.in, got, err)
			}
		}
	})

	t.Run("asFloat64", func(t *testing.T) {
		for _, tc := range []struct {
			in      any
			want    float64
			wantErr bool
		}{
			{float64(1.5), 1.5, false},
			{int64(-2), -2, false},
			{uint64(3), 3, false},
			{int(4), 4, false},
			{"nope", 0, true},
		} {
			got, err := asFloat64(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("asFloat64(%v) = %v, %v", tc.in, got, err)
			}
		}
	})

	t.Run("range checks", func(t *testing.T) {
		if _, err := asInt64Range(int64(200), math.MinInt8, math.MaxInt8); !errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("asInt64Range(200, int8) err = %v", err)
		}
		if _, err := asInt64Range("nope", math.MinInt8, math.MaxInt8); err == nil {
			t.Error("asInt64Range(string) succeeded")
		}
		if _, err := asUint64Range(uint64(300), math.MaxUint8); !errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("asUint64Range(300, uint8) err = %v", err)
		}
		if _, err := asUint64Range("nope", math.MaxUint8); err == nil {
			t.Error("asUint64Range(string) succeeded")
		}
	})
}

// TestAppendValueMismatches covers per-builder wrong-type and out-of-range
// errors, and the alternate integer representations succeeding.
func TestAppendValueMismatches(t *testing.T) {
	newBuilder := func(dt arrow.DataType) array.Builder {
		return array.NewBuilder(memory.DefaultAllocator, dt)
	}
	bad := []struct {
		name  string
		dt    arrow.DataType
		value any
	}{
		{"bool from int", arrow.FixedWidthTypes.Boolean, int64(1)},
		{"int8 overflow", arrow.PrimitiveTypes.Int8, int64(200)},
		{"int16 overflow", arrow.PrimitiveTypes.Int16, int64(1 << 20)},
		{"int32 overflow", arrow.PrimitiveTypes.Int32, int64(1 << 40)},
		{"int64 from string", arrow.PrimitiveTypes.Int64, "x"},
		{"uint8 overflow", arrow.PrimitiveTypes.Uint8, int64(300)},
		{"uint16 overflow", arrow.PrimitiveTypes.Uint16, int64(1 << 20)},
		{"uint32 overflow", arrow.PrimitiveTypes.Uint32, int64(1 << 40)},
		{"uint64 negative", arrow.PrimitiveTypes.Uint64, int64(-1)},
		{"float32 from string", arrow.PrimitiveTypes.Float32, "x"},
		{"float64 from bool", arrow.PrimitiveTypes.Float64, true},
		{"string from int", arrow.BinaryTypes.String, int64(1)},
		{"large string from int", arrow.BinaryTypes.LargeString, int64(1)},
		{"timestamp from string", &arrow.TimestampType{Unit: arrow.Nanosecond}, "x"},
		{"dictionary from int", dictUtf8(), int64(1)},
		{"unsupported builder", arrow.FixedWidthTypes.Date32, int64(1)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuilder(tc.dt)
			defer b.Release()
			if err := appendValue(b, tc.value); err == nil {
				t.Fatalf("appendValue(%T, %v) succeeded", b, tc.value)
			}
		})
	}

	good := []struct {
		name  string
		dt    arrow.DataType
		value any
	}{
		{"int64 from uint64", arrow.PrimitiveTypes.Int64, uint64(7)},
		{"int32 from float64", arrow.PrimitiveTypes.Int32, float64(7)},
		{"uint64 from int64", arrow.PrimitiveTypes.Uint64, int64(7)},
		{"float64 from int64", arrow.PrimitiveTypes.Float64, int64(7)},
		{"float32 from float64", arrow.PrimitiveTypes.Float32, float64(1.5)},
		{"timestamp from uint64", &arrow.TimestampType{Unit: arrow.Second}, uint64(7)},
		{"large string", arrow.BinaryTypes.LargeString, "ok"},
		{"dictionary string", dictUtf8(), "ok"},
		{"null into any", arrow.PrimitiveTypes.Int8, nil},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuilder(tc.dt)
			defer b.Release()
			if err := appendValue(b, tc.value); err != nil {
				t.Fatalf("appendValue(%T, %v) = %v", b, tc.value, err)
			}
		})
	}
}

// TestStringAtVariants covers non-string columns and large-string
// dictionaries.
func TestStringAtVariants(t *testing.T) {
	intBuilder := array.NewBuilder(memory.DefaultAllocator, arrow.PrimitiveTypes.Int64)
	defer intBuilder.Release()
	intBuilder.(*array.Int64Builder).Append(1)
	ints := intBuilder.NewArray()
	defer ints.Release()
	if _, ok := stringAt(ints, 0); ok {
		t.Error("stringAt(int column) succeeded")
	}
	if v := valueAt(ints, 0); v != int64(1) {
		t.Errorf("valueAt(int column) = %v", v)
	}

	// large-string dictionaries are readable (no builder exists, so they are
	// not representable; construct the array directly)
	largeDict := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32,
		ValueType: arrow.BinaryTypes.LargeString}
	indexBuilder := array.NewBuilder(memory.DefaultAllocator, arrow.PrimitiveTypes.Int32)
	defer indexBuilder.Release()
	indexBuilder.(*array.Int32Builder).Append(0)
	indices := indexBuilder.NewArray()
	defer indices.Release()
	wordsBuilder := array.NewBuilder(memory.DefaultAllocator, arrow.BinaryTypes.LargeString)
	defer wordsBuilder.Release()
	wordsBuilder.(*array.LargeStringBuilder).Append("big")
	words := wordsBuilder.NewArray()
	defer words.Release()
	dict := array.NewDictionaryArray(largeDict, indices, words)
	defer dict.Release()
	if v, ok := stringAt(dict, 0); !ok || v != "big" {
		t.Errorf("stringAt(large-string dictionary) = %q, %t", v, ok)
	}

	// an unsupported column type yields nil from the defensive fallthrough
	listBuilder := array.NewBuilder(memory.DefaultAllocator,
		arrow.ListOf(arrow.PrimitiveTypes.Int64))
	defer listBuilder.Release()
	listBuilder.(*array.ListBuilder).Append(true)
	lists := listBuilder.NewArray()
	defer lists.Release()
	if v := valueAt(lists, 0); v != nil {
		t.Errorf("valueAt(list column) = %v, want nil", v)
	}
}

// TestFieldDataTypeMapping covers the Arrow-to-dataset type table, including
// every timestamp unit and the unknown fallback.
func TestFieldDataTypeMapping(t *testing.T) {
	for _, tc := range []struct {
		dt   arrow.DataType
		want timeseries.FieldDataType
	}{
		{arrow.FixedWidthTypes.Boolean, timeseries.Bool},
		{arrow.PrimitiveTypes.Int8, timeseries.Int64},
		{arrow.PrimitiveTypes.Uint32, timeseries.Int64},
		{arrow.PrimitiveTypes.Uint64, timeseries.Uint64},
		{arrow.PrimitiveTypes.Float32, timeseries.Float64},
		{arrow.BinaryTypes.String, timeseries.String},
		{arrow.BinaryTypes.LargeString, timeseries.String},
		{dictUtf8(), timeseries.String},
		{&arrow.TimestampType{Unit: arrow.Second}, timeseries.DateTimeUnixSecs},
		{&arrow.TimestampType{Unit: arrow.Millisecond}, timeseries.DateTimeUnixMilli},
		{&arrow.TimestampType{Unit: arrow.Microsecond}, timeseries.DateTimeUnixMicro},
		{&arrow.TimestampType{Unit: arrow.Nanosecond}, timeseries.DateTimeUnixNano},
		{arrow.FixedWidthTypes.Date32, timeseries.Unknown},
	} {
		if got := fieldDataType(tc.dt); got != tc.want {
			t.Errorf("fieldDataType(%s) = %d, want %d", tc.dt, got, tc.want)
		}
	}
}

// TestToRecordsEdgeCases covers the header-derived timestamp name fallback,
// unrepresentable target schemas, nil series entries, and append failures for
// tag and value columns.
func TestToRecordsEdgeCases(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "host", Type: arrow.BinaryTypes.String},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	rec := makeRecord(t, schema, [][]any{{int64(1), "a", 1.0}})
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ("host"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("timestamp name from series header", func(t *testing.T) {
		ds.TimeRangeQuery = nil
		defer func() { ds.TimeRangeQuery = testTRQ("host") }()
		recs, err := ToRecords(schema, ds)
		if err != nil || len(recs) != 1 {
			t.Fatalf("ToRecords(no trq) = %d batches, %v", len(recs), err)
		}
		extractRows(recs)
	})

	t.Run("unrepresentable schema", func(t *testing.T) {
		bad := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64)},
		}, nil)
		if _, err := ToRecords(bad, ds); !errors.Is(err, ErrNotRepresentable) {
			t.Fatalf("err = %v, want ErrNotRepresentable", err)
		}
		if _, err := ToRecords(nil, ds); !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("nil schema err = %v, want ErrSchemaMismatch", err)
		}
	})

	t.Run("nil series entries are skipped", func(t *testing.T) {
		clone := ds.Clone().(*dataset.DataSet)
		clone.Results[0].SeriesList = append(dataset.SeriesList{nil},
			clone.Results[0].SeriesList...)
		recs, err := ToRecords(schema, clone)
		if err != nil || len(recs) != 1 {
			t.Fatalf("ToRecords(nil series) = %d batches, %v", len(recs), err)
		}
		extractRows(recs)
	})

	t.Run("tag append failure", func(t *testing.T) {
		// the tag's schema column is numeric, so appending the string tag
		// value fails per-column
		numeric := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "host", Type: arrow.PrimitiveTypes.Int64},
			{Name: "v", Type: arrow.PrimitiveTypes.Float64},
		}, nil)
		if _, err := ToRecords(numeric, ds); !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("err = %v, want ErrSchemaMismatch", err)
		}
	})

	t.Run("value append failure", func(t *testing.T) {
		boolValue := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "host", Type: arrow.BinaryTypes.String},
			{Name: "v", Type: arrow.FixedWidthTypes.Boolean},
		}, nil)
		if _, err := ToRecords(boolValue, ds); !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("err = %v, want ErrSchemaMismatch", err)
		}
	})
}

// TestFromRecordsSkipsNilRecords verifies nil batch entries are tolerated.
func TestFromRecordsSkipsNilRecords(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	rec := makeRecord(t, schema, [][]any{{int64(1), 1.0}})
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{nil, rec, nil}, testTRQ())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ds.Results[0].SeriesList[0].Points); n != 1 {
		t.Fatalf("points = %d, want 1", n)
	}
	if _, err := FromRecords(schema, nil, nil); !errors.Is(err, ErrNotRepresentable) {
		t.Fatalf("nil trq err = %v, want ErrNotRepresentable", err)
	}
}
