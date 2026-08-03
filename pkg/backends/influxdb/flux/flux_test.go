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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }

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
	e2 := timeseries.Extent{
		Start: time.Unix(1672531200, 0),
		End:   time.Unix(1673136000, 0),
	}
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
	trq, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
	if err != nil {
		t.Error(err)
	} else {
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != int(timeconv.Day.Hours()) {
			t.Errorf("expected %d got %d", int(timeconv.Day.Hours()), int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
	}
}

func TestParseTimeRangeQueryBranches(t *testing.T) {
	t.Parallel()

	t.Run("unsupported language format", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", nil)
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.InfluxqlGet)
		if !errors.Is(err, iofmt.ErrSupportedQueryLanguage) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("body read error", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", errReader{})
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxRawCsv)
		if err == nil || !strings.Contains(err.Error(), "read fail") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid json body", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader([]byte(`{bad`)))
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
		if err == nil {
			t.Fatal("expected json unmarshal error")
		}
	})

	t.Run("raw flux query", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/?org=my-org",
			bytes.NewReader([]byte(testFluxQuery1)))
		trq, rlo, _, err := ParseTimeRangeQuery(req, iofmt.FluxRawCsv)
		if err != nil {
			t.Fatal(err)
		}
		if trq.CacheKeyElements[ParamOrg] != "my-org" {
			t.Fatalf("org cache key = %q", trq.CacheKeyElements[ParamOrg])
		}
		if rlo == nil || rlo.ProviderRequest == nil {
			t.Fatal("expected provider request body")
		}
	})

	t.Run("non-flux type", func(t *testing.T) {
		b, _ := json.Marshal(JSONRequestBody{Query: testFluxQuery1, Type: "influxql"})
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", bytes.NewReader(b))
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
		if !errors.Is(err, iofmt.ErrSupportedQueryLanguage) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing query", func(t *testing.T) {
		b, _ := json.Marshal(JSONRequestBody{Type: LangFlux})
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", bytes.NewReader(b))
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
		if err == nil || !strings.Contains(err.Error(), AttrQuery) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("parse query error", func(t *testing.T) {
		bad := `from("b") |> range(start: bad, stop: also-bad)`
		b, _ := json.Marshal(JSONRequestBody{Query: bad, Type: LangFlux})
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", bytes.NewReader(b))
		_, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
		if err == nil {
			t.Fatal("expected parse error")
		}
	})

	t.Run("now and params cache keys", func(t *testing.T) {
		b, _ := json.Marshal(JSONRequestBody{
			Query:  testFluxQuery1,
			Type:   LangFlux,
			Now:    "2023-01-01T00:00:00Z",
			Params: map[string]any{"bucket": "metrics", "n": 42},
		})
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/", bytes.NewReader(b))
		trq, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxJSONCsv)
		if err != nil {
			t.Fatal(err)
		}
		if trq.CacheKeyElements[AttrNow] == "" {
			t.Fatal("expected now cache key")
		}
		if trq.CacheKeyElements["fluxParam-bucket"] != "metrics" {
			t.Fatalf("bucket param = %q", trq.CacheKeyElements["fluxParam-bucket"])
		}
		if trq.CacheKeyElements["fluxParam-n"] != "42" {
			t.Fatalf("n param = %q", trq.CacheKeyElements["fluxParam-n"])
		}
	})
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

func TestSetExtentBranches(t *testing.T) {
	q := &Query{
		original:  testFluxQuery1,
		tokenized: testFluxQueryTokenized1,
		step:      time.Minute,
	}
	trq := &timeseries.TimeRangeQuery{Step: q.step}
	start := time.Unix(1672531200, 0).UTC()
	end := time.Unix(1673136000, 0).UTC()
	ext := &timeseries.Extent{Start: start, End: end}

	t.Run("empty range adjusts start", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader([]byte(testFluxQueryTokenized1)))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
		empty := &timeseries.Extent{Start: end, End: end}
		SetExtent(r, trq, empty, q)
		b, _ := io.ReadAll(r.Body)
		wantStart := end.Unix() - int64(trq.Step.Seconds())
		if !strings.Contains(string(b), fmt.Sprintf("start: %d", wantStart)) {
			t.Fatalf("body = %s", b)
		}
	})

	t.Run("empty body logs and returns", func(t *testing.T) {
		buf := &bytes.Buffer{}
		prev := logger.Logger()
		l := logging.StreamLogger(buf, level.Error)
		l.SetLogAsynchronous(false)
		logger.SetLogger(l)
		t.Cleanup(func() { logger.SetLogger(prev) })

		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader(nil))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
		SetExtent(r, trq, ext, q)
		if !strings.Contains(buf.String(), setExtentErrorLogEvent) {
			t.Fatalf("log = %q", buf.String())
		}
	})

	t.Run("body read error logs and returns", func(t *testing.T) {
		buf := &bytes.Buffer{}
		prev := logger.Logger()
		l := logging.StreamLogger(buf, level.Error)
		l.SetLogAsynchronous(false)
		logger.SetLogger(l)
		t.Cleanup(func() { logger.SetLogger(prev) })

		r, _ := http.NewRequest(http.MethodPost, "https://example.com/", errReader{})
		r.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
		SetExtent(r, trq, ext, q)
		if !strings.Contains(buf.String(), setExtentErrorLogEvent) {
			t.Fatalf("log = %q", buf.String())
		}
	})

	t.Run("json content type", func(t *testing.T) {
		body, _ := json.Marshal(JSONRequestBody{
			Query: testFluxQueryTokenized1,
			Type:  LangFlux,
			Now:   "now-token",
		})
		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader(body))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		SetExtent(r, trq, ext, q)
		b, _ := io.ReadAll(r.Body)
		var out JSONRequestBody
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Query, fmt.Sprintf("start: %d", start.Unix())) {
			t.Fatalf("query = %s", out.Query)
		}
		if out.Now != "now-token" {
			t.Fatalf("now = %v", out.Now)
		}
	})

	t.Run("json unmarshal error", func(t *testing.T) {
		buf := &bytes.Buffer{}
		prev := logger.Logger()
		l := logging.StreamLogger(buf, level.Error)
		l.SetLogAsynchronous(false)
		logger.SetLogger(l)
		t.Cleanup(func() { logger.SetLogger(prev) })

		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader([]byte(`{bad`)))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		SetExtent(r, trq, ext, q)
		if !strings.Contains(buf.String(), setExtentErrorLogEvent) {
			t.Fatalf("log = %q", buf.String())
		}
	})

	t.Run("json null body", func(t *testing.T) {
		buf := &bytes.Buffer{}
		prev := logger.Logger()
		l := logging.StreamLogger(buf, level.Error)
		l.SetLogAsynchronous(false)
		logger.SetLogger(l)
		t.Cleanup(func() { logger.SetLogger(prev) })

		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader([]byte("null")))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		SetExtent(r, trq, ext, q)
		if !strings.Contains(buf.String(), setExtentErrorLogEvent) {
			t.Fatalf("log = %q", buf.String())
		}
	})

	t.Run("default content type", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
			bytes.NewReader([]byte("ignored")))
		r.Header.Set(headers.NameContentType, "text/plain")
		SetExtent(r, trq, ext, q)
		if r.Header.Get(headers.NameContentType) != headers.ValueApplicationJSON {
			t.Fatalf("content-type = %q", r.Header.Get(headers.NameContentType))
		}
		b, _ := io.ReadAll(r.Body)
		var out JSONRequestBody
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Query, fmt.Sprintf("stop: %d", end.Unix())) {
			t.Fatalf("query = %s", out.Query)
		}
	})
}
