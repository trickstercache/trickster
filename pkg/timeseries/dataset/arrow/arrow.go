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

// Package arrow converts between Apache Arrow record batches and Trickster's
// provider-neutral dataset.DataSet, enabling delta-proxy caching of Arrow
// responses: batches are decomposed into tag-partitioned series for extent
// merging, and merged datasets are rebuilt into batches conforming to the
// response's original Arrow schema, so column order, types, nullability, and
// timestamp units survive the round trip. Schemas that the dataset model
// cannot express (nested, union, extension, non-string dictionaries, ...)
// are reported by Representable so callers can fall back to byte-verbatim
// caching instead.
package arrow

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

var (
	// ErrNotRepresentable indicates a schema the dataset model cannot express
	// with full fidelity; callers should serve such responses byte-verbatim.
	ErrNotRepresentable = errors.New("schema is not representable as a dataset")
	// ErrNullTimestamp indicates a row with no timestamp value, which cannot
	// be placed on the time axis.
	ErrNullTimestamp = errors.New("null timestamp value")
	// ErrNullTag indicates a null in a tag column; dataset tags cannot
	// distinguish null from empty, so the response is not representable.
	ErrNullTag = errors.New("null tag value")
	// ErrSchemaMismatch indicates a dataset whose series do not carry the
	// fields the target schema requires.
	ErrSchemaMismatch = errors.New("dataset does not match the arrow schema")
)

// maxRowsPerBatch bounds the row count of each rebuilt record batch.
const maxRowsPerBatch = 8192

// SortKey names one result column the rebuilt rows are ordered by, so a
// response reassembled from merged parts reproduces the ordering its query
// asked for.
type SortKey struct {
	// Column is the schema column name to sort on.
	Column string
	// Descending reverses the comparison for this key.
	Descending bool
	// NullsFirst places null values ahead of non-null ones.
	NullsFirst bool
}

// Representable reports whether a schema survives the dataset round trip:
// exactly one column named timestampField with an Arrow timestamp type, and
// every other column a type the dataset value model can express. Anything
// else (nested types, unions, intervals, decimals, non-string dictionaries,
// extension types, ...) must be served byte-verbatim instead.
func Representable(schema *arrow.Schema, timestampField string) bool {
	if schema == nil || timestampField == "" {
		return false
	}
	indices := schema.FieldIndices(timestampField)
	if len(indices) != 1 {
		return false
	}
	for i, field := range schema.Fields() {
		if i == indices[0] {
			if field.Type.ID() != arrow.TIMESTAMP {
				return false
			}
			continue
		}
		if !representableValueType(field.Type) {
			return false
		}
	}
	return true
}

func representableValueType(dt arrow.DataType) bool {
	switch dt.ID() {
	case arrow.BOOL,
		arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
		arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64,
		arrow.FLOAT32, arrow.FLOAT64,
		arrow.STRING, arrow.LARGE_STRING,
		arrow.TIMESTAMP:
		return true
	case arrow.DICTIONARY:
		// IOx-style engines dictionary-encode tag columns; only utf8-valued
		// dictionaries round-trip (indices are re-derived on rebuild).
		// Large-utf8 dictionaries are readable but arrow-go has no builder
		// for them, so they cannot be rebuilt.
		return dt.(*arrow.DictionaryType).ValueType.ID() == arrow.STRING
	}
	return false
}

// stringishType reports whether a column can serve as a dataset tag, whose
// values are stored as strings.
func stringishType(dt arrow.DataType) bool {
	switch dt.ID() {
	case arrow.STRING, arrow.LARGE_STRING, arrow.DICTIONARY:
		return representableValueType(dt)
	}
	return false
}

// FromRecords decomposes Arrow record batches into a tag-partitioned DataSet.
// The timestamp column is named by trq.TimestampDefinition; columns named in
// trq.TagFieldDefintions become series tags (they must be string-typed);
// every other column becomes a value field. Rows for distinct tag tuples land
// in distinct series so same-epoch points never collapse across tags.
func FromRecords(schema *arrow.Schema, records []arrow.RecordBatch,
	trq *timeseries.TimeRangeQuery,
) (*dataset.DataSet, error) {
	if schema == nil || trq == nil {
		return nil, ErrNotRepresentable
	}
	tsName := trq.TimestampDefinition.Name
	if !Representable(schema, tsName) {
		return nil, ErrNotRepresentable
	}
	tsIndex := schema.FieldIndices(tsName)[0]
	tsType := schema.Field(tsIndex).Type.(*arrow.TimestampType)

	tagIndices := make(map[int]bool, len(trq.TagFieldDefintions))
	for _, fd := range trq.TagFieldDefintions {
		for _, index := range schema.FieldIndices(fd.Name) {
			if !stringishType(schema.Field(index).Type) {
				return nil, fmt.Errorf("%w: tag column %q is %s, not string-typed",
					ErrNotRepresentable, fd.Name, schema.Field(index).Type)
			}
			tagIndices[index] = true
		}
	}

	tfd := timeseries.FieldDefinition{
		Name:           tsName,
		DataType:       timestampFieldDataType(tsType),
		SDataType:      tsType.String(),
		OutputPosition: tsIndex,
		Role:           timeseries.RoleTimestamp,
	}
	var tagFields, valueFields timeseries.FieldDefinitions
	var valueIndices []int
	for i, field := range schema.Fields() {
		if i == tsIndex {
			continue
		}
		fd := timeseries.FieldDefinition{
			Name:           field.Name,
			DataType:       fieldDataType(field.Type),
			SDataType:      field.Type.String(),
			OutputPosition: i,
		}
		if tagIndices[i] {
			fd.Role = timeseries.RoleTag
			tagFields = append(tagFields, fd)
		} else {
			fd.Role = timeseries.RoleValue
			valueFields = append(valueFields, fd)
			valueIndices = append(valueIndices, i)
		}
	}

	seriesByKey := make(map[string]*dataset.Series)
	var seriesKeys []string
	for _, rec := range records {
		if rec == nil {
			continue
		}
		for row := range int(rec.NumRows()) {
			ep, ok := timestampAt(rec.Column(tsIndex), row, tsType)
			if !ok {
				return nil, ErrNullTimestamp
			}
			var key string
			tags := dataset.Tags{}
			if len(tagFields) > 0 {
				parts := make([]string, len(tagFields))
				for i, fd := range tagFields {
					value, ok := stringAt(rec.Column(fd.OutputPosition), row)
					if !ok {
						return nil, fmt.Errorf("%w: column %q", ErrNullTag, fd.Name)
					}
					parts[i] = value
					tags[fd.Name] = value
				}
				key = strings.Join(parts, "\x00")
			}
			series, ok := seriesByKey[key]
			if !ok {
				sh := dataset.SeriesHeader{
					Name:            "default",
					TimestampField:  tfd,
					TagFieldsList:   tagFields,
					ValueFieldsList: valueFields,
					Tags:            tags,
					QueryStatement:  trq.Statement,
				}
				sh.CalculateSize()
				series = &dataset.Series{Header: sh}
				seriesByKey[key] = series
				seriesKeys = append(seriesKeys, key)
			}
			values := make([]any, len(valueIndices))
			for i, columnIndex := range valueIndices {
				values[i] = valueAt(rec.Column(columnIndex), row)
			}
			series.Points = append(series.Points, dataset.Point{Epoch: ep, Values: values})
		}
	}

	slices.Sort(seriesKeys)
	seriesList := make(dataset.SeriesList, len(seriesKeys))
	for i, key := range seriesKeys {
		seriesList[i] = seriesByKey[key]
	}
	return &dataset.DataSet{
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
		Results:        dataset.Results{&dataset.Result{SeriesList: seriesList}},
	}, nil
}

// seriesContext pairs a series with the mapping from schema column index to
// the position of that field in the series' point Values (-1 for tag columns).
type seriesContext struct {
	series     *dataset.Series
	valueIndex []int
}

// rowRef locates one output row: its epoch, its series, and its point.
type rowRef struct {
	ep          epoch.Epoch
	seriesIndex int
	point       *dataset.Point
}

// defaultRowOrder is the time-major fallback ordering — ascending epoch, then
// series order — applied when no sort keys are given and to break ties among
// rows the sort keys compare equal.
func defaultRowOrder(a, b rowRef) int {
	if a.ep != b.ep {
		if a.ep < b.ep {
			return -1
		}
		return 1
	}
	return a.seriesIndex - b.seriesIndex
}

// rowComparators compiles sort keys into per-key row comparators bound to the
// schema's columns: the timestamp column compares epochs, tag columns compare
// the series' tag value, and value columns compare the point's value.
func rowComparators(schema *arrow.Schema, tsIndex int, contexts []seriesContext,
	keys []SortKey,
) ([]func(a, b rowRef) int, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]func(a, b rowRef) int, 0, len(keys))
	for _, key := range keys {
		indices := schema.FieldIndices(key.Column)
		if len(indices) != 1 {
			return nil, fmt.Errorf("%w: sort column %q", ErrSchemaMismatch, key.Column)
		}
		column := indices[0]
		sign := 1
		if key.Descending {
			sign = -1
		}
		switch column {
		case tsIndex:
			out = append(out, func(a, b rowRef) int {
				if a.ep == b.ep {
					return 0
				}
				if a.ep < b.ep {
					return -sign
				}
				return sign
			})
		default:
			name := schema.Field(column).Name
			nullsFirst := key.NullsFirst
			out = append(out, func(a, b rowRef) int {
				left := cellValue(contexts, a, column, name)
				right := cellValue(contexts, b, column, name)
				if left == nil || right == nil {
					return nullOrder(left, right, nullsFirst)
				}
				return sign * compareValues(left, right)
			})
		}
	}
	return out, nil
}

// cellValue resolves a row's value for one schema column: the series tag when
// the column is a tag, otherwise the point's value.
func cellValue(contexts []seriesContext, row rowRef, column int, name string) any {
	sc := contexts[row.seriesIndex]
	if sc.valueIndex[column] < 0 {
		return sc.series.Header.Tags[name]
	}
	position := sc.valueIndex[column]
	if position >= len(row.point.Values) {
		return nil
	}
	return row.point.Values[position]
}

// nullOrder places nulls ahead of or behind non-null values independently of
// the term's sort direction, matching SQL NULLS FIRST / NULLS LAST.
func nullOrder(a, b any, nullsFirst bool) int {
	if a == nil && b == nil {
		return 0
	}
	rank := 1
	if nullsFirst {
		rank = -1
	}
	if a == nil {
		return rank
	}
	return -rank
}

// compareValues orders two non-null dataset values from the same column,
// which the value model narrows to bool, string, and the numeric types.
// Mixed representations fall back to their rendered forms.
func compareValues(a, b any) int {
	switch left := a.(type) {
	case string:
		if right, ok := b.(string); ok {
			return strings.Compare(left, right)
		}
	case bool:
		if right, ok := b.(bool); ok {
			switch {
			case left == right:
				return 0
			case right:
				return -1
			}
			return 1
		}
	}
	left, leftErr := asFloat64(a)
	right, rightErr := asFloat64(b)
	if leftErr != nil || rightErr != nil {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	}
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	}
	return 0
}

// ToRecords rebuilds record batches conforming to schema from a DataSet
// produced by FromRecords (possibly delta-merged in between). Rows are sorted
// by the supplied keys, falling back to time-major order — ascending epoch,
// then series order — when none are given; dictionary indices are re-derived,
// which changes framing but not schema or values. A key naming a column the
// schema does not carry is an ErrSchemaMismatch.
func ToRecords(schema *arrow.Schema, ds *dataset.DataSet,
	keys ...SortKey,
) ([]arrow.RecordBatch, error) {
	if ds == nil || schema == nil {
		return nil, ErrSchemaMismatch
	}
	tsName := ""
	if ds.TimeRangeQuery != nil {
		tsName = ds.TimeRangeQuery.TimestampDefinition.Name
	}
	if tsName == "" && len(ds.Results) > 0 && len(ds.Results[0].SeriesList) > 0 {
		tsName = ds.Results[0].SeriesList[0].Header.TimestampField.Name
	}
	if !Representable(schema, tsName) {
		return nil, ErrNotRepresentable
	}
	tsIndex := schema.FieldIndices(tsName)[0]
	tsType := schema.Field(tsIndex).Type.(*arrow.TimestampType)

	var contexts []seriesContext
	if len(ds.Results) > 0 {
		for _, series := range ds.Results[0].SeriesList {
			if series == nil {
				continue
			}
			sc := seriesContext{series: series, valueIndex: make([]int, schema.NumFields())}
			for i, field := range schema.Fields() {
				sc.valueIndex[i] = -1
				if i == tsIndex {
					continue
				}
				if _, isTag := series.Header.Tags[field.Name]; isTag {
					continue
				}
				position := slices.IndexFunc(series.Header.ValueFieldsList,
					func(fd timeseries.FieldDefinition) bool { return fd.Name == field.Name })
				if position < 0 {
					return nil, fmt.Errorf("%w: series lacks column %q",
						ErrSchemaMismatch, field.Name)
				}
				sc.valueIndex[i] = position
			}
			contexts = append(contexts, sc)
		}
	}

	var rows []rowRef
	for seriesIndex, sc := range contexts {
		for i := range sc.series.Points {
			point := &sc.series.Points[i]
			rows = append(rows, rowRef{ep: point.Epoch, seriesIndex: seriesIndex, point: point})
		}
	}
	comparators, err := rowComparators(schema, tsIndex, contexts, keys)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(rows, func(a, b rowRef) int {
		for _, compare := range comparators {
			if c := compare(a, b); c != 0 {
				return c
			}
		}
		return defaultRowOrder(a, b)
	})

	var out []arrow.RecordBatch
	for start := 0; start < len(rows); start += maxRowsPerBatch {
		chunk := rows[start:min(start+maxRowsPerBatch, len(rows))]
		builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
		for _, row := range chunk {
			sc := contexts[row.seriesIndex]
			for i, field := range schema.Fields() {
				fieldBuilder := builder.Field(i)
				switch {
				case i == tsIndex:
					appendTimestamp(fieldBuilder.(*array.TimestampBuilder), row.ep, tsType)
				case sc.valueIndex[i] < 0:
					if err := appendValue(fieldBuilder, sc.series.Header.Tags[field.Name]); err != nil {
						builder.Release()
						return nil, fmt.Errorf("column %q: %w", field.Name, err)
					}
				default:
					if err := appendValue(fieldBuilder, row.point.Values[sc.valueIndex[i]]); err != nil {
						builder.Release()
						return nil, fmt.Errorf("column %q: %w", field.Name, err)
					}
				}
			}
		}
		rec := builder.NewRecordBatch()
		builder.Release()
		out = append(out, rec)
	}
	return out, nil
}

// timestampAt converts a timestamp cell to Unix-nanosecond epoch.
func timestampAt(column arrow.Array, row int, tsType *arrow.TimestampType) (epoch.Epoch, bool) {
	ts, ok := column.(*array.Timestamp)
	if !ok || ts.IsNull(row) {
		return 0, false
	}
	return epoch.Epoch(int64(ts.Value(row)) * int64(tsType.Unit.Multiplier())), true
}

// appendTimestamp writes a Unix-nanosecond epoch as a timestamp in the
// column's own unit.
func appendTimestamp(builder *array.TimestampBuilder, ep epoch.Epoch, tsType *arrow.TimestampType) {
	builder.Append(arrow.Timestamp(int64(ep) / int64(tsType.Unit.Multiplier())))
}

// stringAt extracts a string cell, materializing dictionary-encoded values.
func stringAt(column arrow.Array, row int) (string, bool) {
	if column.IsNull(row) {
		return "", false
	}
	switch value := column.(type) {
	case *array.String:
		return value.Value(row), true
	case *array.LargeString:
		return value.Value(row), true
	case *array.Dictionary:
		index := value.GetValueIndex(row)
		switch words := value.Dictionary().(type) {
		case *array.String:
			return words.Value(index), true
		case *array.LargeString:
			return words.Value(index), true
		}
	}
	return "", false
}

// valueAt extracts a value cell into the dataset's normalized value model:
// nil for null, bool, int64 for signed integers and small unsigned integers,
// uint64 for 64-bit unsigned, float64 for floats, string for strings and
// string dictionaries, and the raw int64 (in the column's own unit) for
// non-axis timestamp columns.
func valueAt(column arrow.Array, row int) any {
	if column.IsNull(row) {
		return nil
	}
	switch value := column.(type) {
	case *array.Boolean:
		return value.Value(row)
	case *array.Int8:
		return int64(value.Value(row))
	case *array.Int16:
		return int64(value.Value(row))
	case *array.Int32:
		return int64(value.Value(row))
	case *array.Int64:
		return value.Value(row)
	case *array.Uint8:
		return int64(value.Value(row))
	case *array.Uint16:
		return int64(value.Value(row))
	case *array.Uint32:
		return int64(value.Value(row))
	case *array.Uint64:
		return value.Value(row)
	case *array.Float32:
		return float64(value.Value(row))
	case *array.Float64:
		return value.Value(row)
	case *array.Timestamp:
		return int64(value.Value(row))
	}
	if s, ok := stringAt(column, row); ok {
		return s
	}
	return nil
}

// appendValue writes a normalized dataset value into an Arrow builder,
// tolerating the integer/float representations the cache serialization layer
// may substitute (e.g. positive int64 values decoding as uint64).
func appendValue(builder array.Builder, value any) error {
	if value == nil {
		builder.AppendNull()
		return nil
	}
	switch b := builder.(type) {
	case *array.BooleanBuilder:
		v, ok := value.(bool)
		if !ok {
			return typeError(value, "bool")
		}
		b.Append(v)
	case *array.Int8Builder:
		v, err := asInt64Range(value, math.MinInt8, math.MaxInt8)
		if err != nil {
			return err
		}
		b.Append(int8(v)) // #nosec G115 -- range-checked above
	case *array.Int16Builder:
		v, err := asInt64Range(value, math.MinInt16, math.MaxInt16)
		if err != nil {
			return err
		}
		b.Append(int16(v)) // #nosec G115 -- range-checked above
	case *array.Int32Builder:
		v, err := asInt64Range(value, math.MinInt32, math.MaxInt32)
		if err != nil {
			return err
		}
		b.Append(int32(v)) // #nosec G115 -- range-checked above
	case *array.Int64Builder:
		v, err := asInt64(value)
		if err != nil {
			return err
		}
		b.Append(v)
	case *array.Uint8Builder:
		v, err := asUint64Range(value, math.MaxUint8)
		if err != nil {
			return err
		}
		b.Append(uint8(v)) // #nosec G115 -- range-checked above
	case *array.Uint16Builder:
		v, err := asUint64Range(value, math.MaxUint16)
		if err != nil {
			return err
		}
		b.Append(uint16(v)) // #nosec G115 -- range-checked above
	case *array.Uint32Builder:
		v, err := asUint64Range(value, math.MaxUint32)
		if err != nil {
			return err
		}
		b.Append(uint32(v)) // #nosec G115 -- range-checked above
	case *array.Uint64Builder:
		v, err := asUint64(value)
		if err != nil {
			return err
		}
		b.Append(v)
	case *array.Float32Builder:
		v, err := asFloat64(value)
		if err != nil {
			return err
		}
		b.Append(float32(v))
	case *array.Float64Builder:
		v, err := asFloat64(value)
		if err != nil {
			return err
		}
		b.Append(v)
	case *array.StringBuilder:
		v, ok := value.(string)
		if !ok {
			return typeError(value, "string")
		}
		b.Append(v)
	case *array.LargeStringBuilder:
		v, ok := value.(string)
		if !ok {
			return typeError(value, "string")
		}
		b.Append(v)
	case *array.TimestampBuilder:
		v, err := asInt64(value)
		if err != nil {
			return err
		}
		b.Append(arrow.Timestamp(v))
	case *array.BinaryDictionaryBuilder:
		v, ok := value.(string)
		if !ok {
			return typeError(value, "string")
		}
		return b.AppendString(v)
	default:
		return fmt.Errorf("%w: unsupported builder %T", ErrNotRepresentable, builder)
	}
	return nil
}

func typeError(value any, want string) error {
	return fmt.Errorf("%w: value %T is not %s", ErrSchemaMismatch, value, want)
}

func asInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, rangeError(value)
		}
		return int64(v), nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	}
	return 0, typeError(value, "integer")
}

func asInt64Range(value any, minValue, maxValue int64) (int64, error) {
	v, err := asInt64(value)
	if err != nil {
		return 0, err
	}
	if v < minValue || v > maxValue {
		return 0, rangeError(value)
	}
	return v, nil
}

func asUint64(value any) (uint64, error) {
	switch v := value.(type) {
	case uint64:
		return v, nil
	case int64:
		if v < 0 {
			return 0, rangeError(value)
		}
		return uint64(v), nil
	case int:
		if v < 0 {
			return 0, rangeError(value)
		}
		return uint64(v), nil
	case float64:
		if v < 0 {
			return 0, rangeError(value)
		}
		return uint64(v), nil
	}
	return 0, typeError(value, "unsigned integer")
}

func asUint64Range(value any, maxValue uint64) (uint64, error) {
	v, err := asUint64(value)
	if err != nil {
		return 0, err
	}
	if v > maxValue {
		return 0, rangeError(value)
	}
	return v, nil
}

func rangeError(value any) error {
	return fmt.Errorf("%w: value %v out of column range", ErrSchemaMismatch, value)
}

func asFloat64(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case int:
		return float64(v), nil
	}
	return 0, typeError(value, "float")
}

// fieldDataType maps an Arrow column type onto the dataset field type model.
func fieldDataType(dt arrow.DataType) timeseries.FieldDataType {
	switch dt.ID() {
	case arrow.BOOL:
		return timeseries.Bool
	case arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
		arrow.UINT8, arrow.UINT16, arrow.UINT32:
		return timeseries.Int64
	case arrow.UINT64:
		return timeseries.Uint64
	case arrow.FLOAT32, arrow.FLOAT64:
		return timeseries.Float64
	case arrow.STRING, arrow.LARGE_STRING, arrow.DICTIONARY:
		return timeseries.String
	case arrow.TIMESTAMP:
		return timestampFieldDataType(dt.(*arrow.TimestampType))
	}
	return timeseries.Unknown
}

func timestampFieldDataType(dt *arrow.TimestampType) timeseries.FieldDataType {
	switch dt.Unit {
	case arrow.Second:
		return timeseries.DateTimeUnixSecs
	case arrow.Millisecond:
		return timeseries.DateTimeUnixMilli
	case arrow.Microsecond:
		return timeseries.DateTimeUnixMicro
	default:
		return timeseries.DateTimeUnixNano
	}
}
