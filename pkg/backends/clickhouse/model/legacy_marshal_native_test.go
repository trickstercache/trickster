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
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// legacyMarshalTimeseriesNative is the Native marshaler the column writers replaced, kept as their oracle
func legacyMarshalTimeseriesNative(w io.Writer, ds *dataset.DataSet, options *timeseries.RequestOptions) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(formatHeader, "Native")
		hw.Header().Set(headers.NameContentType, "application/octet-stream")
	}
	revision := uint64(server.ServerRevision)
	if options != nil {
		if format, ok := options.ProviderRequest.(FormatOptions); ok {
			revision = format.Revision
		}
	}
	if len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		return server.EncodeNativeFormat(w, nil, nil, 0, revision)
	}
	fields, _, _, _ := ds.FieldDefinitions()
	rows, count := timeOrderedRows(ds.Results[0])
	columns := make([]server.Column, len(fields))
	values := make([][]any, len(fields))
	times := make([]nativeTimeFormat, len(fields))
	for i, f := range fields {
		columns[i] = server.Column{Name: f.Name, Type: f.SDataType}
		values[i] = make([]any, 0, count)
		if f.Role == timeseries.RoleTimestamp {
			times[i] = newNativeTimeFormat(f)
		}
	}
	layouts := make(map[*dataset.Series]*nativeSeries, len(ds.Results[0].SeriesList))
	var layout *nativeSeries
	var last *dataset.Series
	for row := range rows {
		series := row.series
		if series != last {
			if layout = layouts[series]; layout == nil {
				layout = newNativeSeries(series, fields)
				layouts[series] = layout
			}
			last = series
		}
		for i, f := range fields {
			var value any
			switch f.Role {
			case timeseries.RoleTimestamp:
				value = times[i].format(row.epoch())
			case timeseries.RoleValue:
				index := layout.index[i]
				if index < 0 || index >= row.seg.NumCols() {
					return timeseries.ErrInvalidBody
				}
				value = row.seg.Value(index, row.i)
			default:
				value = layout.cells[i]
			}
			values[i] = append(values[i], value)
		}
	}
	// encode to memory first so a column that cannot be encoded yields an
	// error instead of a truncated body behind an already-sent status
	var buf bytes.Buffer
	if err := server.EncodeNativeFormat(&buf, columns, values, uint64(count), revision); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
