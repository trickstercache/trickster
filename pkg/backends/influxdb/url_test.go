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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

const untokenized = "SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"

func TestParseTimeRangeQuery(t *testing.T) {
	u := fmt.Sprintf("http://example.com/?q=%s",
		url.QueryEscape(untokenized))
	r, _ := http.NewRequest(http.MethodGet, u, nil)
	c := &Client{}
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Error("expected non-nil time range query")
	}
}

func TestSetExtent(t *testing.T) {
	c := &Client{}
	start := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	end := time.Now().UTC().Truncate(time.Second)
	ext := &timeseries.Extent{Start: start, End: end}

	t.Run("influxql with parsed query", func(t *testing.T) {
		qs := url.Values{"q": {untokenized}}.Encode()
		r, _ := http.NewRequest(http.MethodGet, "http://example.com/?"+qs, nil)
		q, err := influxql.NewParser(strings.NewReader(untokenized)).ParseQuery()
		if err != nil {
			t.Fatal(err)
		}
		trq := &timeseries.TimeRangeQuery{
			Step:        time.Minute,
			ParsedQuery: q,
		}
		c.SetExtent(r, trq, ext)
		if got := r.URL.Query().Get("q"); !strings.Contains(got, "time >=") {
			t.Errorf("expected rewritten query with time bounds, got %q", got)
		}
	})

	t.Run("influxql reparses when parsed query nil", func(t *testing.T) {
		qs := url.Values{"q": {untokenized}}.Encode()
		r, _ := http.NewRequest(http.MethodGet, "http://example.com/?"+qs, nil)
		trq := &timeseries.TimeRangeQuery{Step: time.Minute}
		c.SetExtent(r, trq, ext)
		if trq.ParsedQuery == nil {
			t.Fatal("expected ParsedQuery to be populated")
		}
		if _, ok := trq.ParsedQuery.(*influxql.Query); !ok {
			t.Fatalf("expected *influxql.Query, got %T", trq.ParsedQuery)
		}
		if got := r.URL.Query().Get("q"); !strings.Contains(got, "time >=") {
			t.Errorf("expected rewritten query with time bounds, got %q", got)
		}
	})

	t.Run("parse failure is a no-op", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "http://example.com/?q=not-a-query", nil)
		before := r.URL.RawQuery
		trq := &timeseries.TimeRangeQuery{}
		c.SetExtent(r, trq, ext)
		if r.URL.RawQuery != before {
			t.Errorf("expected request unchanged on parse failure")
		}
		if trq.ParsedQuery != nil {
			t.Error("expected ParsedQuery to remain nil")
		}
	})

	t.Run("flux with parsed query", func(t *testing.T) {
		const fluxQuery = `from("test-bucket")
  |> range(start: -7d, stop: -6d)
  |> aggregateWindow(every: 1m, func: mean)`
		body, _ := json.Marshal(flux.JSONRequestBody{
			Query: fluxQuery,
			Type:  "flux",
		})
		r, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v2/query",
			bytes.NewReader(body))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)

		trq, _, _, err := c.ParseTimeRangeQuery(r)
		if err != nil {
			t.Fatal(err)
		}
		// rebuild body after parse consumes it
		r.Body = io.NopCloser(bytes.NewReader(body))
		c.SetExtent(r, trq, ext)
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), fmt.Sprintf("start: %d", start.Unix())) {
			t.Errorf("expected flux body to include start unix, got %s", string(b))
		}
	})

	t.Run("unknown parsed query type is a no-op", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "http://example.com/?q=SELECT+1", nil)
		before := r.URL.RawQuery
		trq := &timeseries.TimeRangeQuery{ParsedQuery: "not-a-query-object"}
		c.SetExtent(r, trq, ext)
		if r.URL.RawQuery != before {
			t.Errorf("expected request unchanged for unknown ParsedQuery type")
		}
	})
}
