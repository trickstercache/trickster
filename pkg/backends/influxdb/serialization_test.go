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
	"bytes"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testInfluxQLDoc = `{"results":[{"statement_id":0,"series":[` +
	`{"name":"trickster","columns":["time","value"],` +
	`"values":[[1577836800000,0.5]]}]}]}`

const testFluxCSV = `#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,double,string,string
#group,false,false,true,true,false,false,true,true
#default,_result,,,,,,,
,result,table,_start,_stop,_time,_value,_field,_measurement
,,0,2020-01-01T00:00:00Z,2020-01-01T01:00:00Z,2020-01-01T00:00:00Z,1.5,usage,cpu
`

func TestNewModeler(t *testing.T) {
	m := NewModeler()
	if m == nil || m.WireUnmarshaler == nil || m.WireMarshaler == nil ||
		m.WireMarshalWriter == nil || m.WireUnmarshalerReader == nil ||
		m.CacheMarshaler == nil || m.CacheUnmarshaler == nil {
		t.Fatal("expected fully populated modeler")
	}
}

func TestUnmarshalTimeseries(t *testing.T) {
	if _, err := UnmarshalTimeseries(nil, nil); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
	if _, err := UnmarshalTimeseries([]byte{}, &timeseries.TimeRangeQuery{}); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}

	ts, err := UnmarshalTimeseries([]byte(testInfluxQLDoc), &timeseries.TimeRangeQuery{
		Statement: `SELECT mean("value") FROM cpu`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		t.Fatal("expected non-nil timeseries")
	}

	ts, err = UnmarshalTimeseries([]byte(testFluxCSV), &timeseries.TimeRangeQuery{
		Statement: `from("b") ` + flux.FuncRange + `start: -1h)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		t.Fatal("expected non-nil flux timeseries")
	}
}

func TestUnmarshalTimeseriesReader(t *testing.T) {
	if _, err := UnmarshalTimeseriesReader(nil, nil); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
	if _, err := UnmarshalTimeseriesReader(strings.NewReader(testInfluxQLDoc), nil); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}

	ts, err := UnmarshalTimeseriesReader(strings.NewReader(testInfluxQLDoc),
		&timeseries.TimeRangeQuery{Statement: `SELECT mean("value") FROM cpu`})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		t.Fatal("expected non-nil timeseries")
	}

	ts, err = UnmarshalTimeseriesReader(strings.NewReader(testFluxCSV),
		&timeseries.TimeRangeQuery{Statement: `from("b") ` + flux.FuncRange + `start: -1h)`})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		t.Fatal("expected non-nil flux timeseries")
	}
}

func TestMarshalTimeseries(t *testing.T) {
	if _, err := MarshalTimeseries(nil, nil, 200); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
	ts, err := UnmarshalTimeseries([]byte(testInfluxQLDoc), &timeseries.TimeRangeQuery{
		Statement: `SELECT mean("value") FROM cpu`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalTimeseries(ts, nil, 200); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}

	// OutputFormat with IsInfluxQL bit set routes to the influxql marshaler.
	// That marshaler only accepts OutputFormat 0/1 for encoding, so an iofmt
	// InfluxQL flag value exercises the routing branch (and surfaces its
	// format validation error).
	if _, err := MarshalTimeseries(ts, &timeseries.RequestOptions{
		OutputFormat: byte(iofmt.InfluxqlGet),
	}, 200); err != timeseries.ErrUnknownFormat {
		t.Fatalf("expected ErrUnknownFormat from influxql route, got %v", err)
	}

	ts, err = UnmarshalTimeseries([]byte(testFluxCSV), &timeseries.TimeRangeQuery{
		Statement: `from("b") ` + flux.FuncRange + `start: -1h)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := MarshalTimeseries(ts, &timeseries.RequestOptions{
		OutputFormat: byte(iofmt.FluxRawCsv),
	}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("expected marshaled flux bytes")
	}
}

func TestMarshalTimeseriesWriter(t *testing.T) {
	if err := MarshalTimeseriesWriter(nil, nil, 200, nil); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}

	ts, err := UnmarshalTimeseries([]byte(testInfluxQLDoc), &timeseries.TimeRangeQuery{
		Statement: `SELECT mean("value") FROM cpu`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := MarshalTimeseriesWriter(ts, nil, 200, &bytes.Buffer{}); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}
	if err := MarshalTimeseriesWriter(ts, &timeseries.RequestOptions{}, 200, nil); err != errors.ErrBadRequest {
		t.Fatalf("expected ErrBadRequest, got %v", err)
	}

	w := &bytes.Buffer{}
	if err := MarshalTimeseriesWriter(ts, &timeseries.RequestOptions{OutputFormat: 0}, 200, w); err != nil {
		t.Fatal(err)
	}
	if w.Len() == 0 {
		t.Fatal("expected influxql writer output")
	}

	ts, err = UnmarshalTimeseries([]byte(testFluxCSV), &timeseries.TimeRangeQuery{
		Statement: `from("b") ` + flux.FuncRange + `start: -1h)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if err := MarshalTimeseriesWriter(ts, &timeseries.RequestOptions{
		OutputFormat: byte(iofmt.FluxRawCsv),
	}, 200, w); err != nil {
		t.Fatal(err)
	}
	if w.Len() == 0 {
		t.Fatal("expected flux writer output")
	}
}
