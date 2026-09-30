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
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
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
		hw.Header().Set(headers.NameContentType, "application/octet-stream")
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

// a series' cells that are the same on every row, boxed once, and the index of each value field's
// value in a point (-1 when the series has no such field)
type nativeSeries struct {
	cells []any
	index []int
}

func newNativeSeries(series *dataset.Series, fields timeseries.FieldDefinitions) *nativeSeries {
	ns := &nativeSeries{cells: make([]any, len(fields)), index: make([]int, len(fields))}
	for i, f := range fields {
		ns.index[i] = -1
		switch f.Role {
		case timeseries.RoleTimestamp:
		case timeseries.RoleTag:
			value := series.Header.Tags[f.Name]
			if value != nullToken || !strings.HasPrefix(f.SDataType, "Nullable(") {
				ns.cells[i] = value
			}
		case timeseries.RoleValue:
			// the last value field of a name is the one a lookup by name finds
			for j, vf := range series.Header.ValueFieldsList {
				if vf.Name == f.Name {
					ns.index[i] = j
				}
			}
		default:
			ns.cells[i] = f.DefaultValue
		}
	}
	return ns
}

// how a time column's field formats a point's time: with a layout, or as a count of units
type nativeTimeFormat struct {
	layout string
	unit   timeseries.FieldDataType
}

func newNativeTimeFormat(tfd timeseries.FieldDefinition) nativeTimeFormat {
	switch tfd.SDataType {
	case TypeDateTime:
		return nativeTimeFormat{layout: timeconv.SQLDateTimeLayout}
	case TypeDate:
		return nativeTimeFormat{layout: "2006-01-02"}
	}
	if strings.HasPrefix(tfd.SDataType, "DateTime64") {
		precision, _ := strconv.Atoi(strings.TrimSpace(strings.Split(strings.TrimSuffix(strings.TrimPrefix(tfd.SDataType, "DateTime64("), ")"), ",")[0]))
		if precision > 0 && precision <= 9 {
			return nativeTimeFormat{layout: "2006-01-02 15:04:05." + strings.Repeat("0", precision)}
		}
		return nativeTimeFormat{layout: timeconv.SQLDateTimeLayout}
	}
	// otherwise epoch seconds, or the field's finer unit, as a string
	return nativeTimeFormat{unit: tfd.DataType}
}

func (f nativeTimeFormat) format(ep epoch.Epoch) string {
	nanos := int64(ep)
	t := time.Unix(nanos/1e9, nanos%1e9).UTC()
	if f.layout != "" {
		return t.Format(f.layout)
	}
	switch f.unit {
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

// NativeFormatOptions carries the HTTP client's binary result framing revision.
type NativeFormatOptions struct{ Revision uint64 }
