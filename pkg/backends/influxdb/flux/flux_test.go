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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

const fqAbsoluteTimeMS string = `from("test-bucket")
  |> range(start: 2023-01-01T00:00:00.000Z, stop: 2023-01-08T00:00:00.000Z)
  |> window(every: 5m)
  |> mean()
`

const fqAbsoluteTimeTokenized = `from("test-bucket")
  
|> range(<TIMERANGE_TOKEN>)
  
|> window(every: 5m)
  
|> mean()
`

const testFluxQuery1 = `from("test-bucket")
  |> range(start: -7d, stop: -6d)
  |> aggregateWindow(every: 1m, func: mean)`

const testFluxQueryTokenized1 = `from("test-bucket")
  |> range(<TIMERANGE_TOKEN>)
  |> aggregateWindow(every: 1m, func: mean)`

const testFluxJsonTokenized1 = `{"query":"from(\"test-bucket\")\n  |\u003e <TIMERANGE_TOKEN>\n  |\u003e aggregateWindow(every: 1m, func: mean)","type":"flux","dialect":{"annotations":["datatype","group","default"]}}`

func TestParseQuery(t *testing.T) {
	s, e, d, err := ParseQuery(fqAbsoluteTimeMS)
	if s != fqAbsoluteTimeTokenized {
		t.Error("parsing failure", fmt.Sprintf("[%s]", s), fmt.Sprintf("[%s]", fqAbsoluteTimeTokenized))
	}
	if d != time.Minute*5 {
		t.Error("invalid duration", d)
	}
	e2 := timeseries.Extent{Start: time.Unix(1672531200, 0),
		End: time.Unix(1673136000, 0)}
	if !e.Start.Equal(e2.Start) {
		t.Error("invalid extent start")
	}
	if !e.End.Equal(e2.End) {
		t.Error("invalid extent end")
	}
	if err != nil {
		t.Error(err)
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	b, _ := json.Marshal(JSONRequestBody{
		Query: testFluxQuery1,
		Type:  LangFlux,
	})
	req, _ := http.NewRequest(http.MethodPost, "https://blah.com/",
		bytes.NewReader(b))
	req.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	trq, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJsonCsv)
	if err != nil {
		t.Error(err)
	} else {
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != int(timeconv.Day.Hours()) {
			t.Errorf("expected %d got %d", int(timeconv.Day.Hours()), int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
	}
}

func TestSetExtent(t *testing.T) {

	now := time.Now()

	start := now.Add(-7 * 24 * time.Hour)
	end := now.Add(-6 * 24 * time.Hour)

	r, _ := http.NewRequest(http.MethodPost, "",
		io.NopCloser(bytes.NewBufferString(testFluxQueryTokenized1)))
	r.Header.Add(headers.NameContentType, headers.ValueApplicationFlux)

	q := &Query{
		original:  testFluxQuery1,
		tokenized: testFluxQueryTokenized1,
		step:      time.Minute,
	}

	trq := &timeseries.TimeRangeQuery{Step: q.step}
	e := &timeseries.Extent{Start: start, End: end}
	SetExtent(r, trq, e, q)

	newRange := fmt.Sprintf("range(start: %d, stop: %d)", start.Unix(), end.Unix())
	expected := strings.Replace(testFluxJsonTokenized1, "<TIMERANGE_TOKEN>", newRange, 1)
	b, _ := io.ReadAll(r.Body)
	if string(b) != expected {
		t.Errorf("expected %s, got %s", expected, string(b))
	}
}
