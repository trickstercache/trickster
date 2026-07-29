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

package influxql

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	te "github.com/trickstercache/trickster/v2/pkg/errors"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }

const (
	expectedTokenized = "SELECT * FROM some_column WHERE time >= '$START_TIME$' AND time < '$END_TIME$' GROUP BY time(1m)"
	untokenized       = "SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"
)

const testQuery = `SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h ` +
	`GROUP BY time(15s), "cluster" fill(null)`

var testVals = url.Values(map[string][]string{
	"q":     {testQuery},
	"epoch": {"ms"},
})
var testRawQuery = testVals.Encode()

func TestParseTimeRangeQuery(t *testing.T) {
	// test GET
	req := &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{
			Scheme:   "https",
			Host:     "blah.com",
			Path:     "/",
			RawQuery: testRawQuery,
		},
	}

	trq, rlo, canCache, err := ParseTimeRangeQuery(req, iofmt.InfluxqlGet)
	if err != nil {
		t.Error(err)
	} else {
		if trq.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(trq.Step.Seconds()))
		}
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
		if rlo.TimeFormat != 3 {
			t.Errorf("expected epoch flag %d got %d", 3, rlo.TimeFormat)
		}
		if !canCache {
			t.Error("expected canObjectCache true")
		}
	}

	req, _ = http.NewRequest(http.MethodPost, "http://blah.com/", io.NopCloser(strings.NewReader(testRawQuery)))
	req.Header.Set(headers.NameContentLength, strconv.Itoa(len(testRawQuery)))
	req.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	trq, _, _, err = ParseTimeRangeQuery(req, iofmt.InfluxqlPost)
	if err != nil {
		t.Error(err)
	} else {
		if trq.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(trq.Step.Seconds()))
		}
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
		if len(trq.OriginalBody) == 0 {
			t.Error("expected OriginalBody to be set for POST")
		}
	}
}

func TestParseTimeRangeQueryErrors(t *testing.T) {
	tests := []struct {
		name      string
		req       *http.Request
		format    iofmt.Format
		wantErr   error
		cacheable bool
		checkErr  func(error) bool
	}{
		{
			name:    "nil request",
			req:     nil,
			format:  iofmt.InfluxqlGet,
			wantErr: iofmt.ErrSupportedQueryLanguage,
		},
		{
			name:    "unsupported language",
			req:     &http.Request{Method: http.MethodGet, URL: &url.URL{}},
			format:  iofmt.FluxRawCsv,
			wantErr: iofmt.ErrSupportedQueryLanguage,
		},
		{
			name: "unsupported method",
			req: &http.Request{
				Method: http.MethodPut,
				URL:    &url.URL{RawQuery: "q=SELECT%20*%20FROM%20cpu"},
			},
			format:  iofmt.InfluxqlGet,
			wantErr: te.ErrInvalidMethod,
		},
		{
			name: "missing query param",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{RawQuery: "db=telegraf"},
			},
			format: iofmt.InfluxqlGet,
			checkErr: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), ParamQuery)
			},
		},
		{
			name: "parse query error",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{RawQuery: "q=" + url.QueryEscape("not a query")},
			},
			format: iofmt.InfluxqlGet,
			checkErr: func(err error) bool {
				return err != nil
			},
		},
		{
			name: "empty statements",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{RawQuery: "q=" + url.QueryEscape(";")},
			},
			format:    iofmt.InfluxqlGet,
			wantErr:   pe.ErrNotTimeRangeQuery,
			cacheable: true,
		},
		{
			name: "nil condition",
			req: &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{RawQuery: "q=" + url.QueryEscape(
					"SELECT * FROM cpu")},
			},
			format:    iofmt.InfluxqlGet,
			cacheable: true,
			checkErr: func(err error) bool {
				return err != nil
			},
		},
		{
			name: "group by interval error",
			req: &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{RawQuery: "q=" + url.QueryEscape(
					`SELECT mean(value) FROM cpu WHERE time > now()-1h GROUP BY time(bad)`)},
			},
			format:    iofmt.InfluxqlGet,
			cacheable: true,
			checkErr: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "time dimension")
			},
		},
		{
			name: "mismatched steps",
			req: &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{RawQuery: "q=" + url.QueryEscape(
					`SELECT * FROM a WHERE time >= '2020-01-01T00:00:00Z' AND time < '2020-01-01T01:00:00Z' GROUP BY time(1m); `+
						`SELECT * FROM b WHERE time >= '2020-01-01T00:00:00Z' AND time < '2020-01-01T01:00:00Z' GROUP BY time(5m)`)},
			},
			format:    iofmt.InfluxqlGet,
			wantErr:   pe.ErrStepParse,
			cacheable: true,
		},
		{
			name: "condition expr error",
			req: &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{RawQuery: "q=" + url.QueryEscape(
					`SELECT * FROM cpu WHERE time > 'not-a-time' GROUP BY time(1m)`)},
			},
			format:    iofmt.InfluxqlGet,
			cacheable: true,
			checkErr: func(err error) bool {
				return err != nil
			},
		},
		{
			name: "mismatched extents",
			req: &http.Request{
				Method: http.MethodGet,
				URL: &url.URL{RawQuery: "q=" + url.QueryEscape(
					`SELECT * FROM a WHERE time >= '2020-01-01T00:00:00Z' AND time < '2020-01-01T01:00:00Z' GROUP BY time(1m); `+
						`SELECT * FROM b WHERE time >= '2020-01-01T00:00:00Z' AND time < '2020-01-01T02:00:00Z' GROUP BY time(1m)`)},
			},
			format:    iofmt.InfluxqlGet,
			wantErr:   pe.ErrNotTimeRangeQuery,
			cacheable: true,
		},
		{
			name: "post body read error",
			req: func() *http.Request {
				r, _ := http.NewRequest(http.MethodPost, "http://blah.com/", errReader{})
				r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
				r.URL.RawQuery = "q=" + url.QueryEscape(
					`SELECT * FROM cpu WHERE time > now()-1h GROUP BY time(1m)`)
				return r
			}(),
			format: iofmt.InfluxqlPost,
			checkErr: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "read fail")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, canCache, err := ParseTimeRangeQuery(tc.req, tc.format)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
			} else if tc.checkErr != nil {
				if !tc.checkErr(err) {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if canCache != tc.cacheable {
				t.Errorf("expected cacheable=%v got %v", tc.cacheable, canCache)
			}
		})
	}
}

func TestParseTimeRangeQueryPrettyAndZeroStart(t *testing.T) {
	q := `SELECT * FROM cpu WHERE time < now() GROUP BY time(1m)`
	req := &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{RawQuery: url.Values{
			ParamQuery:  {q},
			ParamPretty: {"true"},
			ParamEpoch:  {"s"},
		}.Encode()},
	}
	trq, rlo, canCache, err := ParseTimeRangeQuery(req, iofmt.InfluxqlGet)
	if err != nil {
		t.Fatal(err)
	}
	if !canCache {
		t.Error("expected canObjectCache true")
	}
	if rlo.OutputFormat != 1 {
		t.Errorf("expected pretty OutputFormat 1, got %d", rlo.OutputFormat)
	}
	if rlo.TimeFormat != 4 {
		t.Errorf("expected epoch flag 4, got %d", rlo.TimeFormat)
	}
	if !trq.Extent.Start.Equal(time.Unix(0, 0)) {
		t.Errorf("expected zero-unix start, got %v", trq.Extent.Start)
	}
}

func TestSetExtent(t *testing.T) {
	start := time.Now().UTC().Add(time.Duration(-6) * time.Hour).Truncate(time.Second)
	end := time.Now().UTC().Truncate(time.Second)

	startToken := start.Format(time.RFC3339Nano)
	endToken := end.Add(time.Second * 60).Format(time.RFC3339Nano)

	expected := strings.ReplaceAll(strings.ReplaceAll(expectedTokenized, "$START_TIME$", startToken), "$END_TIME$", endToken)

	qs := url.Values{"q": {untokenized}, "epoch": {"ms"}, "pretty": {"true"}, "chunked": {"true"}}.Encode()

	tu, _ := url.Parse("http://example.com?" + qs)

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{TemplateURL: tu, Step: time.Second * 60, Statement: untokenized}
	e := &timeseries.Extent{Start: start, End: end}
	p := influxql.NewParser(strings.NewReader(trq.Statement))
	q, err := p.ParseQuery()
	if err != nil {
		t.Error(err)
	}
	SetExtent(r, trq, e, q)
	if expected != r.URL.Query().Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, r.URL.Query().Get("q"))
	}
	if r.URL.Query().Get(ParamEpoch) != "ns" {
		t.Errorf("expected epoch=ns, got %q", r.URL.Query().Get(ParamEpoch))
	}
	if r.URL.Query().Get(ParamPretty) != "" || r.URL.Query().Get(ParamChunked) != "" {
		t.Error("expected pretty and chunked params removed")
	}

	r.Method = http.MethodPost
	r.Body = io.NopCloser(bytes.NewBufferString(qs))
	SetExtent(r, trq, e, q)
	v, _, _ := params.GetRequestValues(r)
	if expected != v.Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, v.Get("q"))
	}

	// unsupported method should leave the query unchanged after logging
	r.Method = http.MethodDelete
	before := r.URL.RawQuery
	SetExtent(r, trq, e, q)
	if r.URL.RawQuery != before {
		t.Errorf("expected RawQuery unchanged for unsupported method")
	}

	// non-select statements are skipped without changing query semantics
	showQ, err := influxql.NewParser(strings.NewReader("SHOW DATABASES")).ParseQuery()
	if err != nil {
		t.Fatal(err)
	}
	r.Method = http.MethodGet
	r.URL.RawQuery = "q=SHOW+DATABASES"
	SetExtent(r, trq, e, showQ)
	if !strings.Contains(strings.ToUpper(r.URL.Query().Get("q")), "SHOW") {
		t.Errorf("expected SHOW query retained, got %q", r.URL.Query().Get("q"))
	}
}
