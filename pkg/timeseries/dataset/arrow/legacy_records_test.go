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

package arrow

import (
	"fmt"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// legacyToRecords is ToRecords as it sorted a reference to every row, kept as an oracle
func legacyToRecords(schema *arrow.Schema, ds *dataset.DataSet,
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

	// Fields copies the schema's field list, so it is called once
	fields := schema.Fields()
	var contexts []seriesContext
	var rowCount int
	if len(ds.Results) > 0 {
		seriesList := ds.Results[0].SeriesList
		contexts = make([]seriesContext, 0, len(seriesList))
		// one slab of each per call, cut into a part per series
		indexSlab := make([]int, len(seriesList)*len(fields))
		tagSlab := make([]any, len(seriesList)*len(fields))
		for _, series := range seriesList {
			if series == nil {
				continue
			}
			n := len(contexts) * len(fields)
			sc := seriesContext{
				series: series, valueIndex: indexSlab[n : n+len(fields) : n+len(fields)],
				tagValues: tagSlab[n : n+len(fields) : n+len(fields)],
			}
			for i, field := range fields {
				sc.valueIndex[i] = -1
				if i == tsIndex {
					continue
				}
				if tag, isTag := series.Header.Tags[field.Name]; isTag {
					sc.tagValues[i] = tag
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
			rowCount += series.PointCount()
		}
	}

	rows := make([]rowRef, 0, rowCount)
	for seriesIndex, sc := range contexts {
		segs := sc.series.Segments()
		for k := range segs {
			seg := &segs[k]
			for i, ep := range seg.Epochs() {
				rows = append(rows, rowRef{ep: ep, seriesIndex: seriesIndex, seg: seg, row: i})
			}
		}
	}
	comparators, err := legacyRowComparators(schema, tsIndex, contexts, keys)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(rows, func(a, b rowRef) int {
		for _, compare := range comparators {
			if c := compare(a, b); c != 0 {
				return c
			}
		}
		return legacyDefaultRowOrder(a, b)
	})

	var out []arrow.RecordBatch
	for start := 0; start < len(rows); start += maxRowsPerBatch {
		chunk := rows[start:min(start+maxRowsPerBatch, len(rows))]
		builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
		builder.Reserve(len(chunk))
		fieldBuilders := builder.Fields()
		tsBuilder := fieldBuilders[tsIndex].(*array.TimestampBuilder)
		for _, row := range chunk {
			sc := &contexts[row.seriesIndex]
			for i, fieldBuilder := range fieldBuilders {
				var err error
				switch {
				case i == tsIndex:
					appendTimestamp(tsBuilder, row.ep, tsType)
				case sc.valueIndex[i] < 0:
					err = appendValue(fieldBuilder, sc.tagValues[i])
				default:
					err = appendCell(fieldBuilder, row.seg, sc.valueIndex[i], row.row)
				}
				if err != nil {
					builder.Release()
					return nil, fmt.Errorf("column %q: %w", fields[i].Name, err)
				}
			}
		}
		rec := builder.NewRecordBatch()
		builder.Release()
		out = append(out, rec)
	}
	return out, nil
}

// legacyRowComparators compiles sort keys into row comparators
func legacyRowComparators(schema *arrow.Schema, tsIndex int, contexts []seriesContext,
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
			nullsFirst := key.NullsFirst
			out = append(out, func(a, b rowRef) int {
				left := legacyCellValue(contexts, a, column)
				right := legacyCellValue(contexts, b, column)
				if left == nil || right == nil {
					return nullOrder(left, right, nullsFirst)
				}
				return sign * compareValues(left, right)
			})
		}
	}
	return out, nil
}

// legacyCellValue returns a row's tag or value, boxed
func legacyCellValue(contexts []seriesContext, row rowRef, column int) any {
	sc := &contexts[row.seriesIndex]
	if sc.valueIndex[column] < 0 {
		return sc.tagValues[column]
	}
	position := sc.valueIndex[column]
	if position >= row.seg.NumCols() {
		return nil
	}
	return row.seg.Value(position, row.row)
}

// legacyDefaultRowOrder orders rows by epoch, then series
func legacyDefaultRowOrder(a, b rowRef) int {
	if a.ep != b.ep {
		if a.ep < b.ep {
			return -1
		}
		return 1
	}
	return a.seriesIndex - b.seriesIndex
}
