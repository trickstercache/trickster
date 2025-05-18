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

package influxdb

import (
	"io"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// NewModeler returns a collection of modeling functions for influxdb interoperability
func NewModeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
		WireMarshalWriter:     MarshalTimeseriesWriter,
		WireUnmarshaler:       UnmarshalTimeseries,
		CacheMarshaler:        dataset.MarshalDataSet,
		CacheUnmarshaler:      dataset.UnmarshalDataSet,
	}
}

func UnmarshalTimeseries(data []byte,
	trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if len(data) == 0 || trq == nil {
		return nil, errors.ErrBadRequest
	}
	if strings.Contains(strings.ToLower(trq.Statement), flux.FuncRange) {
		return flux.UnmarshalTimeseries(data, trq)
	}
	return influxql.UnmarshalTimeseries(data, trq)
}

func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if reader == nil || trq == nil {
		return nil, errors.ErrBadRequest
	}
	if strings.Contains(strings.ToLower(trq.Statement), flux.FuncRange) {
		return flux.UnmarshalTimeseriesReader(reader, trq)
	}
	return influxql.UnmarshalTimeseriesReader(reader, trq)
}

func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	if ts == nil || rlo == nil {
		return nil, errors.ErrBadRequest
	}
	if iofmt.Format(rlo.OutputFormat).IsInfluxQL() {
		return influxql.MarshalTimeseries(ts, rlo, status)
	}
	return flux.MarshalTimeseries(ts, rlo, status)
}

func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	if ts == nil || rlo == nil || w == nil {
		return errors.ErrBadRequest
	}
	if rlo.OutputFormat < 4 {
		return influxql.MarshalTimeseriesWriter(ts, rlo, status, w)
	}
	return flux.MarshalTimeseriesWriter(ts, rlo, status, w)
}
