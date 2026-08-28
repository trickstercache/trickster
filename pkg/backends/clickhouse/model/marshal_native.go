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
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// OutputFormatNative is the output format index for ClickHouse Native binary.
const OutputFormatNative byte = 6

// marshalTimeseriesNative writes a DataSet as a ClickHouse Native binary block.
func marshalTimeseriesNative(w io.Writer, ds *dataset.DataSet, options *timeseries.RequestOptions) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(formatHeader, "Native")
		hw.Header().Set("Content-Type", "application/octet-stream")
	}
	revision := uint64(server.ServerRevision)
	if options != nil {
		if format, ok := options.ProviderRequest.(NativeFormatOptions); ok {
			revision = format.Revision
		}
	}
	if len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		return server.EncodeNativeFormat(w, nil, nil, 0, revision)
	}
	fields, _, _, _ := ds.FieldDefinitions()
	columns := make([]server.Column, len(fields))
	values := make([][]any, len(fields))
	for i, f := range fields {
		columns[i] = server.Column{Name: f.Name, Type: f.SDataType}
	}
	count := 0
	for _, series := range ds.Results[0].SeriesList {
		valueIndexes := make(map[string]int, len(series.Header.ValueFieldsList))
		for i, f := range series.Header.ValueFieldsList {
			valueIndexes[f.Name] = i
		}
		for _, point := range series.Points {
			for i, f := range fields {
				var value any
				switch f.Role {
				case timeseries.RoleTimestamp:
					value = formatEpochForType(point.Epoch, f)
				case timeseries.RoleTag:
					value = series.Header.Tags[f.Name]
				case timeseries.RoleValue:
					index, ok := valueIndexes[f.Name]
					if !ok || index >= len(point.Values) {
						return timeseries.ErrInvalidBody
					}
					value = point.Values[index]
				default:
					value = f.DefaultValue
				}
				values[i] = append(values[i], value)
			}
			count++
		}
	}
	return server.EncodeNativeFormat(w, columns, values, uint64(count), revision)
}

func formatEpochForType(ep epoch.Epoch, tfd timeseries.FieldDefinition) string {
	nanos := int64(ep)
	t := time.Unix(nanos/1e9, nanos%1e9).UTC()
	switch tfd.SDataType {
	case TypeDateTime:
		return t.Format(timeconv.SQLDateTimeLayout)
	case TypeDate:
		return t.Format("2006-01-02")
	default:
		if strings.HasPrefix(tfd.SDataType, "DateTime64") {
			precision, _ := strconv.Atoi(strings.TrimSpace(strings.Split(strings.TrimSuffix(strings.TrimPrefix(tfd.SDataType, "DateTime64("), ")"), ",")[0]))
			if precision > 0 && precision <= 9 {
				return t.Format("2006-01-02 15:04:05." + strings.Repeat("0", precision))
			}
			return t.Format(timeconv.SQLDateTimeLayout)
		}
		// Default: epoch seconds as string
		switch tfd.DataType {
		case timeseries.DateTimeUnixMilli:
			return strconv.FormatInt(t.UnixMilli(), 10)
		case timeseries.DateTimeUnixMicro:
			return strconv.FormatInt(t.UnixMicro(), 10)
		case timeseries.DateTimeUnixNano:
			return strconv.FormatInt(t.UnixNano(), 10)
		default:
			return strconv.FormatInt(t.Unix(), 10)
		}
	}
}

// NativeFormatOptions carries the HTTP client's binary result framing revision.
type NativeFormatOptions struct{ Revision uint64 }
