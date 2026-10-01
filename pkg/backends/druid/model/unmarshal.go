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
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
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

// UnmarshalTimeseries converts a native Druid response into DataSet.
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts a native or SQL Druid response into DataSet, as it's read.
func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

// newDecoder returns the stream.Decoder for the response to the query trq describes
func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	if marker, ok := trq.ParsedQuery.(*SQLQueryPlan); ok {
		if marker == nil || marker.Plan == nil {
			return nil, timeseries.ErrInvalidBody
		}
		d := newSQLDecoder(trq, marker)
		return stream.NewJSON(d.walk, d.finish), nil
	}
	plan, ok := trq.ParsedQuery.(*QueryPlan)
	if !ok || plan == nil {
		return nil, timeseries.ErrInvalidBody
	}
	switch plan.QueryType() {
	case queryTimeseries, queryGroupBy, queryTopN:
	default:
		return nil, timeseries.ErrUnknownFormat
	}
	d := newNativeDecoder(trq, plan)
	return stream.NewJSON(d.walk, d.finish), nil
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

func stringsCompare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
