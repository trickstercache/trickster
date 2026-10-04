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
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// the decoders the stream decoders replaced, kept as their conformance oracle

type legacyNativePoint struct {
	timestamp  time.Time
	dimensions map[string]any
	values     map[string]any
	version    any
	rank       int64
}

// legacyUnmarshalTimeseries converts a native Druid response into DataSet.
func legacyUnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return legacyUnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// legacyUnmarshalTimeseriesReader converts a native Druid response into DataSet.
func legacyUnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if sqlPlan, ok := trq.ParsedQuery.(*SQLQueryPlan); ok {
		return legacyUnmarshalSQLTimeseriesReader(reader, trq, sqlPlan)
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

	points, err := legacyNativePoints(document, plan)
	if err != nil {
		return nil, err
	}
	valueNames := legacyResponseValueNames(points, plan)
	fieldTypes := legacyResponseFieldTypes(points, plan.Dimensions(), valueNames)
	result := &dataset.Result{StatementID: 0, Name: plan.QueryType()}
	seriesByTags := make(map[string]*dataset.Series)
	// each series' points, until they're set on it
	seriesPoints := make(map[*dataset.Series]dataset.Points)
	for _, source := range points {
		tags := make(dataset.Tags, len(source.dimensions))
		for _, name := range plan.Dimensions() {
			tags[name] = legacyTagString(source.dimensions[name])
		}
		key := tags.JSON()
		series := seriesByTags[key]
		if series == nil {
			header := legacyBuildSeriesHeader(plan, trq, tags, valueNames, fieldTypes)
			series = &dataset.Series{Header: header}
			seriesByTags[key] = series
			result.SeriesList = append(result.SeriesList, series)
		}
		seriesPoints[series] = append(seriesPoints[series], dataset.Point{
			Epoch:  epoch.Epoch(source.timestamp.UnixNano()),
			Values: legacyPointValues(source, plan, valueNames),
		})
	}
	for series, pts := range seriesPoints {
		series.SetPoints(pts)
	}
	slices.SortFunc(result.SeriesList, func(a, b *dataset.Series) int {
		return legacyStringsCompare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
	})
	ds := &dataset.DataSet{
		Status:         dataSetStatusSuccess,
		Results:        dataset.Results{result},
		TimeRangeQuery: trq,
	}
	ds.Sort()
	return ds, nil
}

func legacyNativePoints(document []map[string]any, plan *QueryPlan) ([]legacyNativePoint, error) {
	out := make([]legacyNativePoint, 0, len(document))
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
			out = append(out, legacyNativePoint{timestamp: timestamp, values: legacyNormalizeMap(values)})
		case queryGroupBy:
			event, ok := outer["event"].(map[string]any)
			if !ok {
				return nil, timeseries.ErrInvalidBody
			}
			dimensions, values := legacySplitDimensions(legacyNormalizeMap(event), plan.Dimensions())
			rank := groupByRanks[timestamp.UnixNano()]
			groupByRanks[timestamp.UnixNano()] = rank + 1
			out = append(out, legacyNativePoint{
				timestamp: timestamp, dimensions: dimensions,
				values: values, version: legacyNormalizeJSONValue(outer["version"]), rank: rank,
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
				dimensions, values := legacySplitDimensions(legacyNormalizeMap(row), plan.Dimensions())
				out = append(out, legacyNativePoint{
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

func legacySplitDimensions(values map[string]any, names []string) (map[string]any, map[string]any) {
	dimensions := make(map[string]any, len(names))
	for _, name := range names {
		dimensions[name] = values[name]
		delete(values, name)
	}
	return dimensions, values
}

func legacyResponseValueNames(points []legacyNativePoint, plan *QueryPlan) []string {
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

func legacyResponseFieldTypes(points []legacyNativePoint, dimensions, values []string) map[string]timeseries.FieldDataType {
	out := make(map[string]timeseries.FieldDataType, len(dimensions)+len(values)+2)
	for _, point := range points {
		for _, name := range dimensions {
			legacyMergeFieldType(out, name, point.dimensions[name])
		}
		for _, name := range values {
			legacyMergeFieldType(out, name, point.values[name])
		}
		legacyMergeFieldType(out, fieldVersion, point.version)
	}
	out[fieldRank] = timeseries.Int64
	return out
}

func legacyMergeFieldType(types map[string]timeseries.FieldDataType, name string, value any) {
	candidate := legacyFieldDataType(value)
	if existing, ok := types[name]; !ok || existing == timeseries.Null || existing == timeseries.Unknown {
		types[name] = candidate
	} else if candidate != timeseries.Null && candidate != timeseries.Unknown && candidate != existing {
		types[name] = timeseries.Unknown
	}
}

func legacyBuildSeriesHeader(plan *QueryPlan, trq *timeseries.TimeRangeQuery,
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

func legacyPointValues(point legacyNativePoint, plan *QueryPlan, valueNames []string) []any {
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

func legacyNormalizeMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = legacyNormalizeJSONValue(value)
	}
	return out
}

func legacyNormalizeJSONValue(value any) any {
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
		return legacyNormalizeMap(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = legacyNormalizeJSONValue(v[i])
		}
		return out
	default:
		return value
	}
}

func legacyFieldDataType(value any) timeseries.FieldDataType {
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

func legacyTagString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(b)
}

func legacyStringsCompare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// legacyUnmarshalSQLTimeseriesReader decodes an array of row objects, or of row arrays after a row
// of column names, which the Grafana Druid plugin asks for
func legacyUnmarshalSQLTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery,
	marker *SQLQueryPlan,
) (timeseries.Timeseries, error) {
	if trq == nil || marker == nil || marker.Plan == nil {
		return nil, timeseries.ErrInvalidBody
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var raw []json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, timeseries.ErrInvalidBody
	}
	if marker.ResponseFormat() == SQLResponseArray {
		return legacyUnmarshalSQLArrayRows(raw, trq, marker)
	}
	columns := make([]string, 0)
	rows := make([]map[string]any, 0, len(raw))
	for i, message := range raw {
		keys, err := legacySqlObjectKeys(message)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			columns = keys
		} else if !legacySameSQLColumns(columns, keys) {
			return nil, timeseries.ErrInvalidBody
		}
		row, err := legacyDecodeSQLRow(message)
		if err != nil {
			return nil, err
		}
		rows = append(rows, legacyNormalizeMap(row))
	}
	return legacySqlRowsToDataSet(columns, rows, trq, marker)
}

func legacyUnmarshalSQLArrayRows(raw []json.RawMessage, trq *timeseries.TimeRangeQuery,
	marker *SQLQueryPlan,
) (timeseries.Timeseries, error) {
	if !marker.Header() || len(raw) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	columns, err := legacyDecodeSQLHeaderRow(raw[0])
	if err != nil || len(columns) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	rows := make([]map[string]any, 0, len(raw)-1)
	for _, message := range raw[1:] {
		values, err := legacyDecodeSQLArrayRow(message)
		if err != nil || len(values) != len(columns) {
			return nil, timeseries.ErrInvalidBody
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = values[i]
		}
		rows = append(rows, row)
	}
	return legacySqlRowsToDataSet(columns, rows, trq, marker)
}

func legacyDecodeSQLHeaderRow(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	columns := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		name, ok := value.(string)
		if !ok || name == "" {
			return nil, timeseries.ErrInvalidBody
		}
		if _, exists := seen[name]; exists {
			return nil, timeseries.ErrInvalidBody
		}
		seen[name] = struct{}{}
		columns[i] = name
	}
	return columns, nil
}

func legacyDecodeSQLArrayRow(raw []byte) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	return values, nil
}

func legacySqlObjectKeys(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, timeseries.ErrInvalidBody
	}
	keys := make([]string, 0, 8)
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, timeseries.ErrInvalidBody
		}
		if _, exists := seen[key]; exists {
			return nil, timeseries.ErrInvalidBody
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return nil, err
		}
	}
	if token, err = decoder.Token(); err != nil {
		return nil, err
	} else if delim, ok := token.(json.Delim); !ok || delim != '}' {
		return nil, timeseries.ErrInvalidBody
	}
	return keys, nil
}

func legacyDecodeSQLRow(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil || row == nil {
		return nil, timeseries.ErrInvalidBody
	}
	return row, nil
}

func legacySameSQLColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, name := range a {
		counts[name]++
	}
	for _, name := range b {
		if counts[name] == 0 {
			return false
		}
		counts[name]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func legacySqlRowsToDataSet(columns []string, rows []map[string]any,
	trq *timeseries.TimeRangeQuery, marker *SQLQueryPlan,
) (*dataset.DataSet, error) {
	if marker == nil || marker.Plan == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if expected := marker.OutputColumns(); len(columns) > 0 && len(expected) > 0 &&
		!legacySameSQLColumns(expected, columns) {
		return nil, timeseries.ErrInvalidBody
	}
	plan := marker.Plan
	if len(rows) == 0 {
		return &dataset.DataSet{
			Status:         dataSetStatusSuccess,
			TimeRangeQuery: trq,
			ExtentList:     timeseries.ExtentList{trq.Extent},
			Results:        dataset.Results{&dataset.Result{Name: sqlResultName, SeriesList: dataset.SeriesList{}}},
		}, nil
	}
	tsIndex := legacySqlColumnIndex(columns, plan.OutputColumn)
	if tsIndex < 0 {
		return nil, timeseries.ErrInvalidBody
	}
	tsName := columns[tsIndex]
	tagIndices := make([]int, len(plan.GroupColumns))
	used := map[int]struct{}{tsIndex: {}}
	for i, name := range plan.GroupColumns {
		index := legacySqlColumnIndex(columns, name)
		if index < 0 {
			return nil, timeseries.ErrInvalidBody
		}
		if _, duplicate := used[index]; duplicate {
			return nil, timeseries.ErrInvalidBody
		}
		used[index] = struct{}{}
		tagIndices[i] = index
	}
	valueIndices := make([]int, 0, len(columns)-len(used))
	for index := range columns {
		if _, excluded := used[index]; !excluded {
			valueIndices = append(valueIndices, index)
		}
	}
	timestamp := timeseries.FieldDefinition{
		Name: tsName, DataType: timeseries.DateTimeRFC3339Nano,
		Role: timeseries.RoleTimestamp, OutputPosition: tsIndex,
	}
	tagFields := make(timeseries.FieldDefinitions, len(tagIndices))
	for i, index := range tagIndices {
		tagFields[i] = timeseries.FieldDefinition{
			Name: columns[index], DataType: timeseries.String,
			Role: timeseries.RoleTag, OutputPosition: index,
		}
	}
	valueFields := make(timeseries.FieldDefinitions, len(valueIndices))
	for i, index := range valueIndices {
		valueFields[i] = timeseries.FieldDefinition{
			// the response holds no types, so a field is untyped in every extent, and each value
			// keeps its own
			Name: columns[index], DataType: timeseries.Unknown,
			Role: timeseries.RoleValue, OutputPosition: index,
		}
	}

	seriesByKey := make(map[string]*dataset.Series)
	// each series' points, until they're set on it
	points := make(map[*dataset.Series]dataset.Points)
	seriesKeys := make([]string, 0, 8)
	result := &dataset.Result{Name: sqlResultName}
	for _, row := range rows {
		ep, err := legacyParseDruidSQLTimestamp(row[tsName])
		if err != nil {
			return nil, err
		}
		tags := make(dataset.Tags, len(tagIndices))
		for _, index := range tagIndices {
			identity, err := legacySqlTagIdentity(row[columns[index]])
			if err != nil {
				return nil, err
			}
			tags[columns[index]] = identity
		}
		key := tags.JSON()
		series := seriesByKey[key]
		if series == nil {
			header := dataset.SeriesHeader{
				Name: sqlResultName, Tags: tags, TimestampField: timestamp,
				TagFieldsList: tagFields, ValueFieldsList: valueFields,
				QueryStatement: trq.Statement,
			}
			header.CalculateSize()
			series = dataset.NewSeries(header, nil)
			seriesByKey[key] = series
			seriesKeys = append(seriesKeys, key)
			result.SeriesList = append(result.SeriesList, series)
		}
		values := make([]any, len(valueIndices))
		for i, index := range valueIndices {
			values[i] = legacyNormalizeJSONValue(row[columns[index]])
		}
		points[series] = append(points[series], dataset.Point{Epoch: ep, Values: values})
	}
	for _, series := range result.SeriesList {
		pts := points[series]
		slices.SortStableFunc(pts, func(a, b dataset.Point) int {
			if a.Epoch < b.Epoch {
				return -1
			}
			if a.Epoch > b.Epoch {
				return 1
			}
			return 0
		})
		series.SetPoints(pts)
	}
	slices.Sort(seriesKeys)
	ordered := make(dataset.SeriesList, 0, len(seriesKeys))
	for _, key := range seriesKeys {
		ordered = append(ordered, seriesByKey[key])
	}
	result.SeriesList = ordered
	return &dataset.DataSet{
		Status: dataSetStatusSuccess, Results: dataset.Results{result},
		TimeRangeQuery: trq, ExtentList: timeseries.ExtentList{trq.Extent},
	}, nil
}

func legacySqlTagIdentity(value any) (string, error) {
	encoded, err := json.Marshal(legacyNormalizeJSONValue(value))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func legacySqlColumnIndex(columns []string, name string) int {
	for i, column := range columns {
		if column == name {
			return i
		}
	}
	for i, column := range columns {
		if strings.EqualFold(column, name) {
			return i
		}
	}
	return -1
}

var legacyDruidSQLTimestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func legacyParseDruidSQLTimestamp(value any) (epoch.Epoch, error) {
	value = legacyNormalizeJSONValue(value)
	switch v := value.(type) {
	case string:
		for _, layout := range legacyDruidSQLTimestampLayouts {
			var parsed time.Time
			var err error
			if strings.Contains(layout, "Z07:00") {
				parsed, err = time.Parse(layout, v)
			} else {
				parsed, err = time.ParseInLocation(layout, v, time.UTC)
			}
			if err == nil && legacyDruidSQLUnixNanoRepresentable(parsed) {
				return epoch.Epoch(parsed.UnixNano()), nil
			}
		}
		if millis, err := strconv.ParseInt(v, 10, 64); err == nil {
			return legacyDruidSQLMillisEpoch(millis)
		}
	case int64:
		return legacyDruidSQLMillisEpoch(v)
	case uint64:
		millis, err := strconv.ParseInt(strconv.FormatUint(v, 10), 10, 64)
		if err == nil {
			return legacyDruidSQLMillisEpoch(millis)
		}
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

func legacyDruidSQLUnixNanoRepresentable(value time.Time) bool {
	return time.Unix(0, value.UnixNano()).UTC().Equal(value.UTC())
}

func legacyDruidSQLMillisEpoch(millis int64) (epoch.Epoch, error) {
	if millis > math.MaxInt64/int64(time.Millisecond) ||
		millis < math.MinInt64/int64(time.Millisecond) {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return epoch.Epoch(millis * int64(time.Millisecond)), nil
}
