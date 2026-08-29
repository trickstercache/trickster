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
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func testTRQ(tags ...string) *timeseries.TimeRangeQuery {
	trq := &timeseries.TimeRangeQuery{
		Statement:           "test",
		TimestampDefinition: timeseries.FieldDefinition{Name: "time", Role: timeseries.RoleTimestamp},
	}
	for _, tag := range tags {
		trq.TagFieldDefintions = append(trq.TagFieldDefintions,
			timeseries.FieldDefinition{Name: tag, Role: timeseries.RoleTag})
	}
	return trq
}

func dictUtf8() arrow.DataType {
	return &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32,
		ValueType: arrow.BinaryTypes.String}
}

// makeRecord builds one record batch from raw row cells. Timestamp cells are
// raw int64 values in the column's own unit; nil cells are nulls.
func makeRecord(t *testing.T, schema *arrow.Schema, rows [][]any) arrow.RecordBatch {
	t.Helper()
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for _, row := range rows {
		for i := range schema.NumFields() {
			if err := appendValue(builder.Field(i), row[i]); err != nil {
				t.Fatalf("append row cell %d: %v", i, err)
			}
		}
	}
	return builder.NewRecordBatch()
}

// extractRows normalizes record batches back into raw row cells for
// comparison, releasing the batches.
func extractRows(recs []arrow.RecordBatch) [][]any {
	var out [][]any
	for _, rec := range recs {
		for row := range int(rec.NumRows()) {
			cells := make([]any, rec.NumCols())
			for i := range int(rec.NumCols()) {
				cells[i] = valueAt(rec.Column(i), row)
			}
			out = append(out, cells)
		}
		rec.Release()
	}
	return out
}

func roundTrip(t *testing.T, schema *arrow.Schema, rows [][]any,
	trq *timeseries.TimeRangeQuery,
) (*dataset.DataSet, [][]any) {
	t.Helper()
	rec := makeRecord(t, schema, rows)
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, trq)
	if err != nil {
		t.Fatalf("FromRecords: %v", err)
	}
	rebuilt, err := ToRecords(schema, ds)
	if err != nil {
		t.Fatalf("ToRecords: %v", err)
	}
	for _, r := range rebuilt {
		if !r.Schema().Equal(schema) {
			t.Fatalf("rebuilt schema differs:\n%v\nvs\n%v", r.Schema(), schema)
		}
	}
	return ds, extractRows(rebuilt)
}

// TestRoundTripAllTypes runs every supported column type, including a
// dictionary-encoded tag, nulls, and precision-sensitive values, through the
// full round trip.
func TestRoundTripAllTypes(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}},
		{Name: "host", Type: dictUtf8()},
		{Name: "f64", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "f32", Type: arrow.PrimitiveTypes.Float32, Nullable: true},
		{Name: "i64", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "i32", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
		{Name: "u64", Type: arrow.PrimitiveTypes.Uint64, Nullable: true},
		{Name: "ok", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
		{Name: "note", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "seen", Type: &arrow.TimestampType{Unit: arrow.Millisecond}, Nullable: true},
	}, nil)

	const ns = int64(1_000_000_000)
	// time-major input so output order matches input order exactly
	rows := [][]any{
		{1700000000 * ns, "a", 1.5, 2.5, int64(-9), int64(-7), uint64(math.MaxUint64), true, "x", int64(1700000000123)},
		{1700000000 * ns, "b", nil, nil, nil, nil, nil, nil, nil, nil},
		{1700000010 * ns, "a", 3.25, 0.25, int64(9007199254740993), int64(42), uint64(1), false, "", int64(0)},
		{1700000010 * ns, "b", 4.0, -1.5, int64(0), int64(-1), uint64(2), true, "y", int64(-5)},
	}

	ds, got := roundTrip(t, schema, rows, testTRQ("host"))

	if n := len(ds.Results[0].SeriesList); n != 2 {
		t.Fatalf("series count = %d, want 2", n)
	}
	for i, wantHost := range []string{"a", "b"} {
		series := ds.Results[0].SeriesList[i]
		if series.Header.Tags["host"] != wantHost || len(series.Points) != 2 {
			t.Fatalf("series %d = tags %v, %d points", i, series.Header.Tags, len(series.Points))
		}
	}
	h0 := ds.Results[0].SeriesList[0].Header.CalculateHash()
	h1 := ds.Results[0].SeriesList[1].Header.CalculateHash()
	if h0 == h1 {
		t.Fatal("distinct tag series share a header hash")
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("round trip changed rows:\n got %v\nwant %v", got, rows)
	}
}

// TestToRecordsTimeMajorOrdering verifies series-major input is re-emitted
// time-major (ascending epoch, series order breaking ties).
func TestToRecordsTimeMajorOrdering(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "host", Type: arrow.BinaryTypes.String},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	const ns = int64(1_000_000_000)
	seriesMajor := [][]any{
		{10 * ns, "a", 1.0}, {20 * ns, "a", 2.0},
		{10 * ns, "b", 3.0}, {20 * ns, "b", 4.0},
	}
	want := [][]any{
		{10 * ns, "a", 1.0}, {10 * ns, "b", 3.0},
		{20 * ns, "a", 2.0}, {20 * ns, "b", 4.0},
	}
	_, got := roundTrip(t, schema, seriesMajor, testTRQ("host"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row order:\n got %v\nwant %v", got, want)
	}
}

func TestRepresentableMatrix(t *testing.T) {
	ts := arrow.Field{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}}
	value := arrow.Field{Name: "v", Type: arrow.PrimitiveTypes.Float64}
	tests := []struct {
		name   string
		fields []arrow.Field
		tsName string
		want   bool
	}{
		{"supported", []arrow.Field{ts, value,
			{Name: "tag", Type: dictUtf8()},
			{Name: "u", Type: arrow.PrimitiveTypes.Uint64},
			{Name: "s", Type: arrow.BinaryTypes.LargeString},
			{Name: "other_ts", Type: &arrow.TimestampType{Unit: arrow.Second}}}, "time", true},
		{"missing ts", []arrow.Field{value}, "time", false},
		{"ts not timestamp", []arrow.Field{
			{Name: "time", Type: arrow.PrimitiveTypes.Int64}, value}, "time", false},
		{"duplicate ts name", []arrow.Field{ts, ts, value}, "time", false},
		{"empty ts name", []arrow.Field{ts, value}, "", false},
		{"list column", []arrow.Field{ts,
			{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64)}}, "time", false},
		{"struct column", []arrow.Field{ts,
			{Name: "st", Type: arrow.StructOf(value)}}, "time", false},
		{"float16 column", []arrow.Field{ts,
			{Name: "h", Type: arrow.FixedWidthTypes.Float16}}, "time", false},
		{"duration column", []arrow.Field{ts,
			{Name: "d", Type: arrow.FixedWidthTypes.Duration_ns}}, "time", false},
		{"non-string dictionary", []arrow.Field{ts,
			{Name: "di", Type: &arrow.DictionaryType{
				IndexType: arrow.PrimitiveTypes.Int32,
				ValueType: arrow.PrimitiveTypes.Int64}}}, "time", false},
		{"large-string dictionary", []arrow.Field{ts,
			{Name: "dl", Type: &arrow.DictionaryType{
				IndexType: arrow.PrimitiveTypes.Int32,
				ValueType: arrow.BinaryTypes.LargeString}}}, "time", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema := arrow.NewSchema(tc.fields, nil)
			if got := Representable(schema, tc.tsName); got != tc.want {
				t.Fatalf("Representable() = %t, want %t", got, tc.want)
			}
		})
	}
	if Representable(nil, "time") {
		t.Fatal("Representable(nil) = true")
	}
}

func TestFromRecordsFailureModes(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}, Nullable: true},
		{Name: "host", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
	}, nil)

	t.Run("null timestamp", func(t *testing.T) {
		rec := makeRecord(t, schema, [][]any{{nil, "a", 1.0}})
		defer rec.Release()
		_, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ("host"))
		if !errors.Is(err, ErrNullTimestamp) {
			t.Fatalf("err = %v, want ErrNullTimestamp", err)
		}
	})

	t.Run("null tag", func(t *testing.T) {
		rec := makeRecord(t, schema, [][]any{{int64(1), nil, 1.0}})
		defer rec.Release()
		_, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ("host"))
		if !errors.Is(err, ErrNullTag) {
			t.Fatalf("err = %v, want ErrNullTag", err)
		}
	})

	t.Run("non-string tag column", func(t *testing.T) {
		numTag := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "shard", Type: arrow.PrimitiveTypes.Int64},
		}, nil)
		rec := makeRecord(t, numTag, [][]any{{int64(1), int64(2)}})
		defer rec.Release()
		_, err := FromRecords(numTag, []arrow.RecordBatch{rec}, testTRQ("shard"))
		if !errors.Is(err, ErrNotRepresentable) {
			t.Fatalf("err = %v, want ErrNotRepresentable", err)
		}
	})

	t.Run("unrepresentable schema", func(t *testing.T) {
		bad := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64)},
		}, nil)
		if _, err := FromRecords(bad, nil, testTRQ()); !errors.Is(err, ErrNotRepresentable) {
			t.Fatalf("err = %v, want ErrNotRepresentable", err)
		}
	})
}

func TestToRecordsSchemaMismatch(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	rec := makeRecord(t, schema, [][]any{{int64(1), 1.0}})
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ())
	if err != nil {
		t.Fatal(err)
	}
	wider := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
		{Name: "extra", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	if _, err := ToRecords(wider, ds); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("err = %v, want ErrSchemaMismatch", err)
	}
	if _, err := ToRecords(schema, nil); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("nil dataset err = %v, want ErrSchemaMismatch", err)
	}
}

func TestEmptyRecords(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "v", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	ds, err := FromRecords(schema, nil, testTRQ())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ds.Results[0].SeriesList); n != 0 {
		t.Fatalf("series count = %d, want 0", n)
	}
	recs, err := ToRecords(schema, ds)
	if err != nil || len(recs) != 0 {
		t.Fatalf("ToRecords(empty) = %d batches, %v", len(recs), err)
	}
}

// TestChunking verifies large datasets are re-emitted across multiple bounded
// record batches with no rows lost.
func TestChunking(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "v", Type: arrow.PrimitiveTypes.Int64},
	}, nil)
	rows := make([][]any, maxRowsPerBatch+10)
	for i := range rows {
		rows[i] = []any{int64(i) * 1_000_000_000, int64(i)}
	}
	rec := makeRecord(t, schema, rows)
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ())
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ToRecords(schema, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt) != 2 {
		t.Fatalf("batch count = %d, want 2", len(rebuilt))
	}
	if got := extractRows(rebuilt); !reflect.DeepEqual(got, rows) {
		t.Fatal("chunked round trip changed rows")
	}
}

// TestRoundTripRandomized property-tests the round trip over randomized
// schemas and values drawn from the supported type pool.
func TestRoundTripRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	valueTypes := []arrow.DataType{
		arrow.FixedWidthTypes.Boolean,
		arrow.PrimitiveTypes.Int8, arrow.PrimitiveTypes.Int16,
		arrow.PrimitiveTypes.Int32, arrow.PrimitiveTypes.Int64,
		arrow.PrimitiveTypes.Uint8, arrow.PrimitiveTypes.Uint16,
		arrow.PrimitiveTypes.Uint32, arrow.PrimitiveTypes.Uint64,
		arrow.PrimitiveTypes.Float32, arrow.PrimitiveTypes.Float64,
		arrow.BinaryTypes.String, arrow.BinaryTypes.LargeString,
		&arrow.TimestampType{Unit: arrow.Millisecond}, dictUtf8(),
	}
	randomCell := func(dt arrow.DataType) any {
		switch dt.ID() {
		case arrow.BOOL:
			return rng.Intn(2) == 0
		case arrow.INT8:
			return int64(int8(rng.Int()))
		case arrow.INT16:
			return int64(int16(rng.Int()))
		case arrow.INT32:
			return int64(int32(rng.Int()))
		case arrow.INT64, arrow.TIMESTAMP:
			return rng.Int63() - rng.Int63()
		case arrow.UINT8:
			return int64(uint8(rng.Int()))
		case arrow.UINT16:
			return int64(uint16(rng.Int()))
		case arrow.UINT32:
			return int64(uint32(rng.Int()))
		case arrow.UINT64:
			return rng.Uint64()
		case arrow.FLOAT32:
			return float64(float32(rng.NormFloat64()))
		case arrow.FLOAT64:
			return rng.NormFloat64()
		default: // string-ish
			return fmt.Sprintf("s%d", rng.Intn(1000))
		}
	}

	for iteration := range 25 {
		fields := []arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}},
			{Name: "tag", Type: arrow.BinaryTypes.String},
		}
		for i := range 1 + rng.Intn(6) {
			fields = append(fields, arrow.Field{
				Name:     fmt.Sprintf("v%d", i),
				Type:     valueTypes[rng.Intn(len(valueTypes))],
				Nullable: true,
			})
		}
		schema := arrow.NewSchema(fields, nil)

		tagValues := []string{"a", "b", "c"}[:1+rng.Intn(3)]
		rowCount := rng.Intn(50)
		rows := make([][]any, rowCount)
		for r := range rows {
			cells := make([]any, len(fields))
			// time-major, unique (epoch, tag) pairs: row r covers epoch
			// r/len(tagValues) for tag r%len(tagValues)
			cells[0] = int64(r/len(tagValues)) * 1_000_000_000
			cells[1] = tagValues[r%len(tagValues)]
			for c := 2; c < len(fields); c++ {
				if rng.Float64() < 0.2 {
					cells[c] = nil
					continue
				}
				cells[c] = randomCell(fields[c].Type)
			}
			rows[r] = cells
		}

		_, got := roundTrip(t, schema, rows, testTRQ("tag"))
		if len(got) == 0 && len(rows) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, rows) {
			t.Fatalf("iteration %d: round trip changed rows\n got %v\nwant %v",
				iteration, got, rows)
		}
	}
}

// orderingSchema is a time/tag/value shape covering every sort-key kind.
func orderingSchema() *arrow.Schema {
	return arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: arrow.FixedWidthTypes.Timestamp_ns},
		{Name: "host", Type: arrow.BinaryTypes.String},
		{Name: "cpu", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
	}, nil)
}

func orderingDataSet(t *testing.T, schema *arrow.Schema, rows [][]any) *dataset.DataSet {
	t.Helper()
	rec := makeRecord(t, schema, rows)
	defer rec.Release()
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ("host"))
	if err != nil {
		t.Fatalf("FromRecords: %v", err)
	}
	return ds
}

func TestToRecordsSortKeys(t *testing.T) {
	schema := orderingSchema()
	rows := [][]any{
		{int64(1000), "a", 2.0},
		{int64(1000), "b", 1.0},
		{int64(2000), "a", 4.0},
		{int64(2000), "b", 3.0},
	}
	tests := []struct {
		name string
		keys []SortKey
		want [][]any
	}{
		{"default time-major", nil, rows},
		{"time descending", []SortKey{{Column: "time", Descending: true}},
			[][]any{
				{int64(2000), "a", 4.0},
				{int64(2000), "b", 3.0},
				{int64(1000), "a", 2.0},
				{int64(1000), "b", 1.0},
			}},
		{"tag then time", []SortKey{{Column: "host"}, {Column: "time"}},
			[][]any{
				{int64(1000), "a", 2.0},
				{int64(2000), "a", 4.0},
				{int64(1000), "b", 1.0},
				{int64(2000), "b", 3.0},
			}},
		{"tag descending then time descending",
			[]SortKey{{Column: "host", Descending: true}, {Column: "time", Descending: true}},
			[][]any{
				{int64(2000), "b", 3.0},
				{int64(1000), "b", 1.0},
				{int64(2000), "a", 4.0},
				{int64(1000), "a", 2.0},
			}},
		{"value ascending", []SortKey{{Column: "cpu"}},
			[][]any{
				{int64(1000), "b", 1.0},
				{int64(1000), "a", 2.0},
				{int64(2000), "b", 3.0},
				{int64(2000), "a", 4.0},
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := orderingDataSet(t, schema, rows)
			recs, err := ToRecords(schema, ds, tc.keys...)
			if err != nil {
				t.Fatalf("ToRecords: %v", err)
			}
			if got := extractRows(recs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestToRecordsSortKeyNullPlacement(t *testing.T) {
	schema := orderingSchema()
	rows := [][]any{
		{int64(1000), "a", 2.0},
		{int64(2000), "b", nil},
		{int64(3000), "c", 1.0},
	}
	tests := []struct {
		name string
		key  SortKey
		want []any
	}{
		{"ascending nulls last", SortKey{Column: "cpu"}, []any{1.0, 2.0, nil}},
		{"ascending nulls first", SortKey{Column: "cpu", NullsFirst: true},
			[]any{nil, 1.0, 2.0}},
		{"descending nulls first", SortKey{Column: "cpu", Descending: true, NullsFirst: true},
			[]any{nil, 2.0, 1.0}},
		{"descending nulls last", SortKey{Column: "cpu", Descending: true},
			[]any{2.0, 1.0, nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := orderingDataSet(t, schema, rows)
			recs, err := ToRecords(schema, ds, tc.key)
			if err != nil {
				t.Fatalf("ToRecords: %v", err)
			}
			got := make([]any, 0, len(tc.want))
			for _, row := range extractRows(recs) {
				got = append(got, row[2])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("cpu order = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestToRecordsSortKeyUnknownColumn(t *testing.T) {
	schema := orderingSchema()
	ds := orderingDataSet(t, schema, [][]any{{int64(1000), "a", 2.0}})
	_, err := ToRecords(schema, ds, SortKey{Column: "nope"})
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("err = %v, want %v", err, ErrSchemaMismatch)
	}
}

func TestCompareValues(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want int
	}{
		{"strings", "a", "b", -1},
		{"equal strings", "a", "a", 0},
		{"bools", false, true, -1},
		{"equal bools", true, true, 0},
		{"bool reversed", true, false, 1},
		{"ints", int64(1), int64(2), -1},
		{"mixed numerics", int64(2), 1.5, 1},
		{"unsigned", uint64(3), int64(3), 0},
		{"incomparable falls back to rendering", "1", int64(1), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareValues(tc.a, tc.b); got != tc.want {
				t.Errorf("compareValues(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
