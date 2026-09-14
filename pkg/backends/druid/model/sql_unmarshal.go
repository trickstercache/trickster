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

// unmarshalSQLTimeseriesReader decodes the supported Druid SQL response
// envelopes. Object responses are arrays of objects; array responses carry a
// first row of column names when requested by the Grafana Druid plugin.
func unmarshalSQLTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery,
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
		return unmarshalSQLArrayRows(raw, trq, marker)
	}
	columns := make([]string, 0)
	rows := make([]map[string]any, 0, len(raw))
	for i, message := range raw {
		keys, err := sqlObjectKeys(message)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			columns = keys
		} else if !sameSQLColumns(columns, keys) {
			return nil, timeseries.ErrInvalidBody
		}
		row, err := decodeSQLRow(message)
		if err != nil {
			return nil, err
		}
		rows = append(rows, normalizeMap(row))
	}
	return sqlRowsToDataSet(columns, rows, trq, marker)
}

func unmarshalSQLArrayRows(raw []json.RawMessage, trq *timeseries.TimeRangeQuery,
	marker *SQLQueryPlan,
) (timeseries.Timeseries, error) {
	if !marker.Header() || len(raw) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	columns, err := decodeSQLHeaderRow(raw[0])
	if err != nil || len(columns) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	rows := make([]map[string]any, 0, len(raw)-1)
	for _, message := range raw[1:] {
		values, err := decodeSQLArrayRow(message)
		if err != nil || len(values) != len(columns) {
			return nil, timeseries.ErrInvalidBody
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = values[i]
		}
		rows = append(rows, row)
	}
	return sqlRowsToDataSet(columns, rows, trq, marker)
}

func decodeSQLHeaderRow(raw []byte) ([]string, error) {
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

func decodeSQLArrayRow(raw []byte) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	return values, nil
}

func sqlObjectKeys(raw []byte) ([]string, error) {
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

func decodeSQLRow(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil || row == nil {
		return nil, timeseries.ErrInvalidBody
	}
	return row, nil
}

func sameSQLColumns(a, b []string) bool {
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

func sqlRowsToDataSet(columns []string, rows []map[string]any,
	trq *timeseries.TimeRangeQuery, marker *SQLQueryPlan,
) (*dataset.DataSet, error) {
	if marker == nil || marker.Plan == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if expected := marker.OutputColumns(); len(columns) > 0 && len(expected) > 0 &&
		!sameSQLColumns(expected, columns) {
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
	tsIndex := sqlColumnIndex(columns, plan.OutputColumn)
	if tsIndex < 0 {
		return nil, timeseries.ErrInvalidBody
	}
	tsName := columns[tsIndex]
	tagIndices := make([]int, len(plan.GroupColumns))
	used := map[int]struct{}{tsIndex: {}}
	for i, name := range plan.GroupColumns {
		index := sqlColumnIndex(columns, name)
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
			// Druid's object and header-array responses do not carry SQL type
			// metadata. Keep the definition stable across independently fetched
			// extents (including an all-null extent) and preserve the concrete
			// JSON value on each point.
			Name: columns[index], DataType: timeseries.Unknown,
			Role: timeseries.RoleValue, OutputPosition: index,
		}
	}

	seriesByKey := make(map[string]*dataset.Series)
	seriesKeys := make([]string, 0, 8)
	result := &dataset.Result{Name: sqlResultName}
	for _, row := range rows {
		ep, err := parseDruidSQLTimestamp(row[tsName])
		if err != nil {
			return nil, err
		}
		tags := make(dataset.Tags, len(tagIndices))
		for _, index := range tagIndices {
			identity, err := sqlTagIdentity(row[columns[index]])
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
			series = &dataset.Series{Header: header}
			seriesByKey[key] = series
			seriesKeys = append(seriesKeys, key)
			result.SeriesList = append(result.SeriesList, series)
		}
		values := make([]any, len(valueIndices))
		for i, index := range valueIndices {
			values[i] = normalizeJSONValue(row[columns[index]])
		}
		point := dataset.Point{Epoch: ep, Values: values}
		point.Size = pointSize(values)
		series.Points = append(series.Points, point)
		series.PointSize += int64(point.Size)
	}
	for _, series := range result.SeriesList {
		slices.SortStableFunc(series.Points, func(a, b dataset.Point) int {
			if a.Epoch < b.Epoch {
				return -1
			}
			if a.Epoch > b.Epoch {
				return 1
			}
			return 0
		})
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

func sqlTagIdentity(value any) (string, error) {
	encoded, err := json.Marshal(normalizeJSONValue(value))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func sqlColumnIndex(columns []string, name string) int {
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

var druidSQLTimestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseDruidSQLTimestamp(value any) (epoch.Epoch, error) {
	value = normalizeJSONValue(value)
	switch v := value.(type) {
	case string:
		for _, layout := range druidSQLTimestampLayouts {
			var parsed time.Time
			var err error
			if strings.Contains(layout, "Z07:00") {
				parsed, err = time.Parse(layout, v)
			} else {
				parsed, err = time.ParseInLocation(layout, v, time.UTC)
			}
			if err == nil && druidSQLUnixNanoRepresentable(parsed) {
				return epoch.Epoch(parsed.UnixNano()), nil
			}
		}
		if millis, err := strconv.ParseInt(v, 10, 64); err == nil {
			return druidSQLMillisEpoch(millis)
		}
	case int64:
		return druidSQLMillisEpoch(v)
	case uint64:
		millis, err := strconv.ParseInt(strconv.FormatUint(v, 10), 10, 64)
		if err == nil {
			return druidSQLMillisEpoch(millis)
		}
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

func druidSQLUnixNanoRepresentable(value time.Time) bool {
	return time.Unix(0, value.UnixNano()).UTC().Equal(value.UTC())
}

func druidSQLMillisEpoch(millis int64) (epoch.Epoch, error) {
	if millis > math.MaxInt64/int64(time.Millisecond) ||
		millis < math.MinInt64/int64(time.Millisecond) {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return epoch.Epoch(millis * int64(time.Millisecond)), nil
}
