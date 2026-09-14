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

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	queryTimeseries = "timeseries"
	queryGroupBy    = "groupby"
	queryTopN       = "topn"

	fieldNativeDimension byte = iota + 1
	fieldNativeVersion
	fieldNativeRank
)

const (
	fieldTimestamp = "timestamp"
	fieldVersion   = "__trickster_druid_version"
	fieldRank      = "__trickster_druid_rank"
)

type nativePoint struct {
	timestamp  time.Time
	dimensions map[string]any
	values     map[string]any
	version    any
	rank       int64
}

// UnmarshalTimeseries converts a native Druid response into DataSet.
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts a native Druid response into DataSet.
func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if sqlPlan, ok := trq.ParsedQuery.(*SQLQueryPlan); ok {
		return unmarshalSQLTimeseriesReader(reader, trq, sqlPlan)
	}
	plan, ok := trq.ParsedQuery.(*QueryPlan)
	if !ok || plan == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if !slices.Contains([]string{queryTimeseries, queryGroupBy, queryTopN}, plan.QueryType()) {
		return nil, timeseries.ErrUnknownFormat
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var document []map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, timeseries.ErrInvalidBody
	}

	points, err := nativePoints(document, plan)
	if err != nil {
		return nil, err
	}
	valueNames := responseValueNames(points, plan)
	fieldTypes := responseFieldTypes(points, plan.Dimensions(), valueNames)
	result := &dataset.Result{StatementID: 0, Name: plan.QueryType()}
	seriesByTags := make(map[string]*dataset.Series)
	for _, source := range points {
		tags := make(dataset.Tags, len(source.dimensions))
		for _, name := range plan.Dimensions() {
			tags[name] = tagString(source.dimensions[name])
		}
		key := tags.JSON()
		series := seriesByTags[key]
		if series == nil {
			header := buildSeriesHeader(plan, trq, tags, valueNames, fieldTypes)
			series = &dataset.Series{Header: header}
			seriesByTags[key] = series
			result.SeriesList = append(result.SeriesList, series)
		}
		values := pointValues(source, plan, valueNames)
		point := dataset.Point{
			Epoch:  epoch.Epoch(source.timestamp.UnixNano()),
			Values: values,
			Size:   pointSize(values),
		}
		series.Points = append(series.Points, point)
		series.PointSize += int64(point.Size)
	}
	slices.SortFunc(result.SeriesList, func(a, b *dataset.Series) int {
		return stringsCompare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
	})
	ds := &dataset.DataSet{
		Status:         dataSetStatusSuccess,
		Results:        dataset.Results{result},
		TimeRangeQuery: trq,
	}
	ds.Sort()
	return ds, nil
}

func nativePoints(document []map[string]any, plan *QueryPlan) ([]nativePoint, error) {
	out := make([]nativePoint, 0, len(document))
	groupByRanks := make(map[int64]int64)
	for _, outer := range document {
		timestampText, ok := outer[fieldTimestamp].(string)
		if !ok {
			return nil, timeseries.ErrInvalidBody
		}
		timestamp, err := time.Parse(time.RFC3339Nano, timestampText)
		if err != nil {
			return nil, err
		}
		switch plan.QueryType() {
		case queryTimeseries:
			values, ok := outer["result"].(map[string]any)
			if !ok {
				return nil, timeseries.ErrInvalidBody
			}
			out = append(out, nativePoint{timestamp: timestamp, values: normalizeMap(values)})
		case queryGroupBy:
			event, ok := outer["event"].(map[string]any)
			if !ok {
				return nil, timeseries.ErrInvalidBody
			}
			dimensions, values := splitDimensions(normalizeMap(event), plan.Dimensions())
			rank := groupByRanks[timestamp.UnixNano()]
			groupByRanks[timestamp.UnixNano()] = rank + 1
			out = append(out, nativePoint{
				timestamp: timestamp, dimensions: dimensions,
				values: values, version: normalizeJSONValue(outer["version"]), rank: rank,
			})
		case queryTopN:
			rows, ok := outer["result"].([]any)
			if !ok {
				return nil, timeseries.ErrInvalidBody
			}
			for rank, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					return nil, timeseries.ErrInvalidBody
				}
				dimensions, values := splitDimensions(normalizeMap(row), plan.Dimensions())
				out = append(out, nativePoint{
					timestamp: timestamp, dimensions: dimensions,
					values: values, rank: int64(rank),
				})
			}
		default:
			return nil, timeseries.ErrUnknownFormat
		}
	}
	return out, nil
}

func splitDimensions(values map[string]any, names []string) (map[string]any, map[string]any) {
	dimensions := make(map[string]any, len(names))
	for _, name := range names {
		dimensions[name] = values[name]
		delete(values, name)
	}
	return dimensions, values
}

func responseValueNames(points []nativePoint, plan *QueryPlan) []string {
	out := plan.ValueFields()
	extra := make(map[string]struct{})
	for _, point := range points {
		for name := range point.values {
			if !slices.Contains(out, name) {
				extra[name] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(extra))
	for name := range extra {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return append(out, keys...)
}

func responseFieldTypes(points []nativePoint, dimensions, values []string) map[string]timeseries.FieldDataType {
	out := make(map[string]timeseries.FieldDataType, len(dimensions)+len(values)+2)
	for _, point := range points {
		for _, name := range dimensions {
			mergeFieldType(out, name, point.dimensions[name])
		}
		for _, name := range values {
			mergeFieldType(out, name, point.values[name])
		}
		mergeFieldType(out, fieldVersion, point.version)
	}
	out[fieldRank] = timeseries.Int64
	return out
}

func mergeFieldType(types map[string]timeseries.FieldDataType, name string, value any) {
	candidate := fieldDataType(value)
	if existing, ok := types[name]; !ok || existing == timeseries.Null || existing == timeseries.Unknown {
		types[name] = candidate
	} else if candidate != timeseries.Null && candidate != timeseries.Unknown && candidate != existing {
		types[name] = timeseries.Unknown
	}
}

func buildSeriesHeader(plan *QueryPlan, trq *timeseries.TimeRangeQuery,
	tags dataset.Tags, valueNames []string,
	fieldTypes map[string]timeseries.FieldDataType,
) dataset.SeriesHeader {
	dimensions := plan.Dimensions()
	tagFields := make(timeseries.FieldDefinitions, len(dimensions))
	valueFields := make(timeseries.FieldDefinitions, 0, len(dimensions)+len(valueNames)+2)
	position := 1
	for i, name := range dimensions {
		tagFields[i] = timeseries.FieldDefinition{
			Name: name, DataType: fieldTypes[name],
			Role: timeseries.RoleTag, OutputPosition: position,
		}
		valueFields = append(valueFields, timeseries.FieldDefinition{
			Name:     name,
			DataType: fieldTypes[name], Role: timeseries.RoleValue,
			OutputPosition: position, ProviderData1: fieldNativeDimension,
		})
		position++
	}
	if plan.QueryType() == queryGroupBy {
		valueFields = append(valueFields, timeseries.FieldDefinition{
			Name:     fieldVersion,
			DataType: fieldTypes[fieldVersion], Role: timeseries.RoleValue,
			OutputPosition: position, ProviderData1: fieldNativeVersion,
		})
		position++
		valueFields = append(valueFields, timeseries.FieldDefinition{
			Name:     fieldRank,
			DataType: timeseries.Int64, Role: timeseries.RoleValue,
			OutputPosition: position, ProviderData1: fieldNativeRank,
		})
		position++
	}
	if plan.QueryType() == queryTopN {
		valueFields = append(valueFields, timeseries.FieldDefinition{
			Name:     fieldRank,
			DataType: timeseries.Int64, Role: timeseries.RoleValue,
			OutputPosition: position, ProviderData1: fieldNativeRank,
		})
		position++
	}
	for _, name := range valueNames {
		valueFields = append(valueFields, timeseries.FieldDefinition{
			Name:     name,
			DataType: fieldTypes[name], Role: timeseries.RoleValue, OutputPosition: position,
		})
		position++
	}
	header := dataset.SeriesHeader{
		Name:            plan.QueryType(),
		Tags:            tags,
		TagFieldsList:   tagFields,
		ValueFieldsList: valueFields,
		TimestampField: timeseries.FieldDefinition{
			Name:     fieldTimestamp,
			DataType: timeseries.DateTimeRFC3339Nano, Role: timeseries.RoleTimestamp,
			OutputPosition: 0,
		},
		QueryStatement: trq.Statement,
	}
	header.CalculateSize()
	return header
}

func pointValues(point nativePoint, plan *QueryPlan, valueNames []string) []any {
	out := make([]any, 0, len(plan.Dimensions())+len(valueNames)+2)
	for _, name := range plan.Dimensions() {
		out = append(out, point.dimensions[name])
	}
	if plan.QueryType() == queryGroupBy {
		out = append(out, point.version)
		out = append(out, point.rank)
	}
	if plan.QueryType() == queryTopN {
		out = append(out, point.rank)
	}
	for _, name := range valueNames {
		out = append(out, point.values[name])
	}
	return out
}

func normalizeMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = normalizeJSONValue(value)
	}
	return out
}

func normalizeJSONValue(value any) any {
	switch v := value.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return u
		}
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return f
		}
		return string(v)
	case map[string]any:
		return normalizeMap(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = normalizeJSONValue(v[i])
		}
		return out
	default:
		return value
	}
}

func fieldDataType(value any) timeseries.FieldDataType {
	switch value.(type) {
	case nil:
		return timeseries.Null
	case int, int8, int16, int32, int64:
		return timeseries.Int64
	case uint, uint8, uint16, uint32, uint64:
		return timeseries.Uint64
	case float32, float64:
		return timeseries.Float64
	case string:
		return timeseries.String
	case bool:
		return timeseries.Bool
	default:
		return timeseries.Unknown
	}
}

func tagString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(b)
}

func pointSize(values []any) int {
	b, _ := json.Marshal(values)
	return len(b) + 32
}

func stringsCompare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
