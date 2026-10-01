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
  |> aggregateWindow(every: 5m, fn: mean)
`

const fqAbsoluteTimeTokenized = `from("test-bucket")
  
|> range(<TIMERANGE_TOKEN>)
  
|> aggregateWindow(every: 5m, fn: mean)
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

// TestParseQuery_Now verifies that `now()` as a range bound is accepted and
// resolved to the current time. This is Grafana's default `stop` value for
// Flux queries — without this, aggregateWindow dashboards fall through to
// HTTPProxy instead of being delta-proxy cached.
func TestParseQueryRefusesCrossBucketStages(t *testing.T) {
	const source = `from(bucket: "b") |> range(start: -1h, stop: now()) |> filter(fn: (r) => r._measurement == "m")`
	const window = ` |> aggregateWindow(every: 1m, fn: mean)`
	tests := []struct {
		name, query string
		expected    error
	}{
		{"windowed", source + window, nil},
		{"per-row stages after the window", source + window + ` |> map(fn: (r) => ({r with _value: r._value * 2.0}))`, nil},
		{"fill with a value", source + window + ` |> fill(value: 0.0)`, nil},
		{"fill without previous", source + window + ` |> fill(usePrevious: false)`, nil},
		{"reducer before the window", source + ` |> max()` + window, nil},
		{"limit", source + window + ` |> limit(n: 5)`, ErrCrossBucket},
		{"tail", source + window + ` |> tail(n: 5)`, ErrCrossBucket},
		{"derivative", source + window + ` |> derivative(unit: 1m)`, ErrCrossBucket},
		{"difference", source + window + ` |> difference()`, ErrCrossBucket},
		{"cumulativeSum", source + window + ` |> cumulativeSum()`, ErrCrossBucket},
		{"movingAverage", source + window + ` |> movingAverage(n: 3)`, ErrCrossBucket},
		{"elapsed", source + ` |> elapsed(unit: 1s)` + window, ErrCrossBucket},
		{"fill previous", source + window + ` |> fill(usePrevious: true)`, ErrCrossBucket},
		{"fill previous across lines", source + window + " |> fill(\n  usePrevious: true\n)", ErrCrossBucket},
		{"reducer after the window", source + window + ` |> max()`, ErrCrossBucket},
		{"sort after the window", source + window + ` |> sort(columns: ["_value"])`, ErrCrossBucket},
		{"timeShift", source + window + ` |> timeShift(duration: 30s)`, ErrUnsupportedWindow},
		{
			"two sources", `a = ` + source + window + "\nb = " + source + window +
				"\njoin(tables: {a: a, b: b}, on: [\"_time\"])", ErrMultipleSources,
		},
		{"one source with spaces", `from (bucket: "b") |> range(start: -1h, stop: now())` + window, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := parseQuery(test.query); !errors.Is(err, test.expected) {
				t.Fatalf("expected %v got %v", test.expected, err)
			}
		})
	}
}

func TestParseQuery_Now(t *testing.T) {
	before := time.Now()
	q := `from(bucket: "trickster") |> range(start: -1h, stop: now()) |> aggregateWindow(every: 1m, fn: mean)`
	_, e, d, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("ParseQuery(now()): %v", err)
	}
	if d != time.Minute {
		t.Errorf("step: got %v, want 1m", d)
	}
	if e.Start.IsZero() || e.End.IsZero() {
		t.Fatalf("extent should be populated, got %+v", e)
	}
	if e.End.Before(before) {
		t.Errorf("now() should resolve to ~now, got %v (before=%v)", e.End, before)
	}
	// start is relative -1h, end is now: window should be ~1h.
	window := e.End.Sub(e.Start)
	if window < 55*time.Minute || window > 65*time.Minute {
		t.Errorf("expected ~1h window, got %v", window)
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
		// the first stop-time window label is one step after the range start
		want := int((timeconv.Day - time.Minute).Minutes())
		if got := int(trq.Extent.End.Sub(trq.Extent.Start).Minutes()); got != want {
			t.Errorf("expected %d minutes got %d", want, got)
		}
		if trq.SampleModel != timeseries.SampleModelBucketStop {
			t.Errorf("expected stop-labeled bucket sample model, got %d", trq.SampleModel)
		}
	}
}

func TestParseTimeRangeQueryWindowLabels(t *testing.T) {
	parse := func(t *testing.T, query string) *timeseries.TimeRangeQuery {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "https://blah.com/",
			strings.NewReader(query))
		req.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
		trq, _, _, err := ParseTimeRangeQuery(req, iofmt.FluxRawCsv)
		if err != nil {
			t.Fatal(err)
		}
		return trq
	}
	const rangeLine = `from(bucket: "b") |> range(start: 1704067200, stop: 1704153600) `
	start, end := time.Unix(1704067200, 0).UTC(), time.Unix(1704153600, 0).UTC()

	t.Run("start-time labels keep the range start", func(t *testing.T) {
		trq := parse(t, rangeLine+
			`|> aggregateWindow(every: 1h, fn: mean, timeSrc: "_start", offset: 15m)`)
		if !trq.Extent.Start.Equal(start) || !trq.Extent.End.Equal(end) {
			t.Errorf("expected extent %s-%s got %s", start, end, trq.Extent)
		}
		if trq.Phase != 15*time.Minute {
			t.Errorf("expected phase 15m got %s", trq.Phase)
		}
		q := trq.ParsedQuery.(*Query)
		if !q.labelsAtStart {
			t.Error("expected start-time labels")
		}
	})

	t.Run("stop-time labels start one step later", func(t *testing.T) {
		trq := parse(t, rangeLine+`|> aggregateWindow(every: 1h, fn: mean)`)
		if !trq.Extent.Start.Equal(start.Add(time.Hour)) {
			t.Errorf("expected start %s got %s", start.Add(time.Hour), trq.Extent.Start)
		}
		// the requested range is the client's range(), before the label shift
		if !trq.Requested.Start.Equal(start) || !trq.Requested.End.Equal(end) || trq.Requested.EndInclusive {
			t.Errorf("unexpected requested range %+v", trq.Requested)
		}
		if trq.StepAlignments != stepAlignments || trq.StepAlignment != timeseries.StepAlignmentTruncate {
			t.Errorf("step alignment = %s of %s", trq.StepAlignment, trq.StepAlignments)
		}
	})

	t.Run("range shorter than a step is clamped", func(t *testing.T) {
		trq := parse(t, `from(bucket: "b") |> range(start: 1704067200, stop: 1704067230) `+
			`|> aggregateWindow(every: 1h, fn: mean)`)
		if !trq.Extent.Start.Equal(trq.Extent.End) {
			t.Errorf("expected an empty extent, got %s", trq.Extent)
		}
	})

	t.Run("raw query without windows is not bucketed", func(t *testing.T) {
		trq := parse(t, rangeLine+`|> filter(fn: (r) => r._field == "v")`)
		if trq.SampleModel != timeseries.SampleModelInstant || !trq.Extent.Start.Equal(start) {
			t.Errorf("unexpected raw query parse: model %d extent %s", trq.SampleModel,
				trq.Extent)
		}
	})
}

func TestSetExtentWindowLabels(t *testing.T) {
	start := time.Unix(1704067200, 0).UTC()
	end := start.Add(3 * time.Hour)
	tests := []struct {
		name          string
		labelsAtStart bool
		ext           timeseries.Extent
		expected      string
	}{
		{
			"stop-time labels", false,
			timeseries.Extent{Start: start, End: end},
			"range(start: 1704063600, stop: 1704078000)",
		},
		{
			"start-time labels", true,
			timeseries.Extent{Start: start, End: end},
			"range(start: 1704067200, stop: 1704081600)",
		},
		{
			"single window", false,
			timeseries.Extent{Start: end, End: end},
			"range(start: 1704074400, stop: 1704078000)",
		},
		{
			"sub-second bounds use time literals", true,
			timeseries.Extent{
				Start: start.Add(500 * time.Millisecond), End: start.Add(500 * time.Millisecond),
			},
			"range(start: 2024-01-01T00:00:00.5Z, stop: 2024-01-01T01:00:00.5Z)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := &Query{
				tokenized: testFluxQueryTokenized1, step: time.Hour,
				labelsAtStart: test.labelsAtStart,
			}
			trq := &timeseries.TimeRangeQuery{Step: time.Hour, ParsedQuery: q}
			r, _ := http.NewRequest(http.MethodPost, "https://example.com/",
				strings.NewReader(testFluxQueryTokenized1))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
			SetExtent(r, trq, &test.ext, q)
			b, _ := io.ReadAll(r.Body)
			var out JSONRequestBody
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.Query, test.expected) {
				t.Errorf("expected %s in %s", test.expected, out.Query)
			}
			// the _start and _stop output columns report the same range bounds
			trq.Extent = test.ext
			rs, rp := q.rangeBounds(test.ext, time.Hour)
			if re := rangeExtent(trq); !re.Start.Equal(rs) || !re.End.Equal(rp) {
				t.Errorf("expected range extent %s-%s got %s", rs, rp, re)
			}
		})
	}

	if e := rangeExtent(nil); !e.Start.IsZero() || !e.End.IsZero() {
		t.Error("expected a zero extent for a nil query")
	}
	plain := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{Start: start, End: end}}
	if e := rangeExtent(plain); !e.Start.Equal(start) || !e.End.Equal(end) {
		t.Errorf("expected the query extent without a parsed flux query, got %s", e)
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
	now := time.Now().Truncate(time.Second)

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

	// stop-time labels start..end come from windows beginning one step before start
	newRange := fmt.Sprintf("range(start: %d, stop: %d)", start.Add(-q.step).Unix(), end.Unix())
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
		if !strings.Contains(out.Query, fmt.Sprintf("start: %d", start.Add(-q.step).Unix())) {
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
