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

package flux

import (
	"bytes"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type marshaler func(*dataset.DataSet, *JSONRequestBody, int, io.Writer) error

const (
	RFC3339Nano = "RFC3339Nano"
	RFC3339     = "RFC3339"

	tableColumnName   = "table"
	startColumnName   = "_start"
	stopColumnName    = "_stop"
	resultColumnName  = "result"
	timeColumnName    = "time"
	timeAltColumnName = "_time"

	TypeString       = "string"
	TypeLong         = "long"
	TypeUnsignedLong = "unsignedLong"
	TypeDouble       = "double"
	TypeBool         = "bool"
	TypeDuration     = "duration"
	TypeRFC3339      = "dateTime:RFC3339"
	TypeRFC3339Nano  = "dateTime:RFC3339Nano"
	TypeNull         = "null"

	sTrue  = "true"
	sFalse = "false"
)

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries,
	ro *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := MarshalTimeseriesWriter(ts, ro, status, buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MarshalTimeseriesWriter writes a Timeseries out to an io.Writer in the desired format.
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	ro *timeseries.RequestOptions, status int, w io.Writer) error {
	ds, frb, m, err := validateMarshalerOptions(ts, ro)
	if err != nil {
		return err
	}
	return m(ds, frb, status, w)
}

func validateMarshalerOptions(ts timeseries.Timeseries,
	ro *timeseries.RequestOptions) (*dataset.DataSet, *JSONRequestBody,
	marshaler, error) {
	if ts == nil {
		return nil, nil, nil, timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, nil, nil, timeseries.ErrUnknownFormat
	}
	var f iofmt.Format
	var frb *JSONRequestBody
	if ro != nil {
		f = iofmt.Format(ro.OutputFormat)
		if ro.ProviderRequest != nil {
			var ok bool
			frb, ok = ro.ProviderRequest.(*JSONRequestBody)
			if !ok {
				frb = DefaultJSONRequestBody()
			}
		}
	}
	if frb == nil {
		frb = DefaultJSONRequestBody()
	}
	if frb.Dialect.DateTimeFormat != RFC3339Nano {
		frb.Dialect.DateTimeFormat = RFC3339
	}
	if f.IsFluxOutputJSON() {
		return ds, frb, marshalTimeseriesJSONWriter, nil
	}
	return ds, frb, marshalTimeseriesCSVWriter, nil
}

func setStartStopTimes(fds timeseries.FieldDefinitions, e timeseries.Extent) {
	// this sets some flux-implied defaults on the series
	for i, fd := range fds {
		if fd.Role != timeseries.RoleUntracked {
			continue
		}
		switch fd.Name {
		case startColumnName:
			fds[i].DefaultValue =
				epoch.FormatTime(e.Start, fd.DataType, false)
		case stopColumnName:
			fds[i].DefaultValue =
				epoch.FormatTime(e.End, fd.DataType, false)
		}
	}
}
