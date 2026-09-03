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

package druid

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const testInterval = "2024-01-01T00:00:00Z/2024-01-02T00:00:00Z"

func druidQuery(queryType, granularity string) string {
	return fmt.Sprintf(`{"queryType":%q,"dataSource":"wiki","intervals":[%q],"granularity":%s,"aggregations":[{"type":"count","name":"count"}]}`,
		queryType, testInterval, granularity)
}

func parseTestQuery(t *testing.T, body string) (*Client, *http.Request,
	*model.QueryPlan, bool, error,
) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2",
		strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	c := &Client{}
	trq, _, canOPC, err := c.ParseTimeRangeQuery(r)
	if trq == nil {
		return c, r, nil, canOPC, err
	}
	plan, _ := trq.ParsedQuery.(*model.QueryPlan)
	return c, r, plan, canOPC, err
}

func reasonOf(t *testing.T, err error) analysisReason {
	t.Helper()
	var classified *classifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("error %v is not classified", err)
	}
	return classified.reason
}

func TestParseTimeRangeQueryFixedSimpleGranularities(t *testing.T) {
	tests := map[string]time.Duration{
		"second": time.Second, "minute": time.Minute,
		"five_minute": 5 * time.Minute, "ten_minute": 10 * time.Minute,
		"fifteen_minute": 15 * time.Minute, "thirty_minute": 30 * time.Minute,
		"hour": time.Hour, "six_hour": 6 * time.Hour,
		"eight_hour": 8 * time.Hour, "day": 24 * time.Hour,
	}
	for granularity, wantStep := range tests {
		t.Run(granularity, func(t *testing.T) {
			body := druidQuery("timeseries", strconvQuote(granularity))
			r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, options, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
			if err != nil {
				t.Fatal(err)
			}
			if !canOPC || trq.Step != wantStep || trq.StepNS != wantStep.Nanoseconds() {
				t.Fatalf("got step=%s stepNS=%d canOPC=%t", trq.Step, trq.StepNS, canOPC)
			}
			if options == nil || !options.FastForwardDisable || options.ProviderRequest == nil {
				t.Fatalf("unexpected request options: %+v", options)
			}
			if trq.CacheKeyElements["dataSource"] != "wiki" {
				t.Fatalf("dataSource key = %q", trq.CacheKeyElements["dataSource"])
			}
			if !strings.Contains(trq.Statement, intervalPlaceholder) {
				t.Fatalf("canonical statement has no interval placeholder: %s", trq.Statement)
			}
			if string(trq.OriginalBody) != body {
				t.Fatal("original body was not retained exactly")
			}
		})
	}
}

func strconvQuote(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func TestParseTimeRangeQueryNonFixedSimpleGranularitiesUseOPC(t *testing.T) {
	for _, granularity := range []string{"all", "none", "week", "month", "quarter", "year"} {
		t.Run(granularity, func(t *testing.T) {
			body := druidQuery("timeseries", strconvQuote(granularity))
			r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
			if err == nil || !canOPC || reasonOf(t, err) != reasonNonFixedGranularity {
				t.Fatalf("got canOPC=%t err=%v", canOPC, err)
			}
			if trq == nil || strings.Contains(trq.Statement, intervalPlaceholder) ||
				!strings.Contains(trq.Statement, testInterval) {
				t.Fatalf("OPC statement lost its real interval: %#v", trq)
			}
		})
	}
}

func TestParseTimeRangeQueryStructuredGranularities(t *testing.T) {
	tests := []struct {
		name, granularity string
		step              time.Duration
		phase             time.Duration
		wantReason        analysisReason
	}{
		{"duration", `{"type":"duration","duration":90000}`, 90 * time.Second, 0, ""},
		{"duration string", `{"type":"duration","duration":"60000"}`, time.Minute, 0, ""},
		{"duration origin", `{"type":"duration","duration":3600000,"origin":"2024-01-01T00:30:00Z"}`, time.Hour, 30 * time.Minute, ""},
		{"period second", `{"type":"period","period":"PT1S"}`, time.Second, 0, ""},
		{"period fraction", `{"type":"period","period":"PT0.750S","timeZone":"UTC"}`, 750 * time.Millisecond, 0, ""},
		{"period compound", `{"type":"period","period":"P1DT2H30M"}`, 26*time.Hour + 30*time.Minute, 0, ""},
		{"period week", `{"type":"period","period":"P1W"}`, 7 * 24 * time.Hour, 4 * 24 * time.Hour, ""},
		{"period month", `{"type":"period","period":"P1M"}`, 0, 0, reasonNonFixedGranularity},
		{"period year", `{"type":"period","period":"P1Y"}`, 0, 0, reasonNonFixedGranularity},
		{"period timezone", `{"type":"period","period":"PT1H","timeZone":"America/Los_Angeles"}`, 0, 0, reasonNonFixedGranularity},
		{"period invalid timezone", `{"type":"period","period":"PT1H","timeZone":7}`, 0, 0, reasonUnsupportedGranularity},
		{"invalid duration", `{"type":"duration","duration":0}`, 0, 0, reasonUnsupportedGranularity},
		{"invalid period", `{"type":"period","period":"PT"}`, 0, 0, reasonUnsupportedGranularity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := druidQuery("timeseries", test.granularity)
			r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
			if test.wantReason != "" {
				if err == nil || !canOPC || reasonOf(t, err) != test.wantReason {
					t.Fatalf("got canOPC=%t err=%v", canOPC, err)
				}
				return
			}
			if err != nil || trq.Step != test.step || trq.Phase != test.phase {
				t.Fatalf("got step=%s phase=%s err=%v", trq.Step, trq.Phase, err)
			}
		})
	}
}

func TestParseTimeRangeQueryTypesAndDimensions(t *testing.T) {
	tests := []struct {
		queryType, extra string
		dimensions       []string
	}{
		{"timeseries", ``, nil},
		{"groupBy", `,"dimensions":["country",{"type":"default","dimension":"device","outputName":"device_name"}]`, []string{"country", "device_name"}},
		{"topN", `,"dimension":{"type":"listFiltered","delegate":{"type":"default","dimension":"page","outputName":"page_name"},"values":["A"]}`, []string{"page_name"}},
	}
	for _, test := range tests {
		t.Run(test.queryType, func(t *testing.T) {
			body := fmt.Sprintf(`{"queryType":%q,"dataSource":"wiki","intervals":[%q],"granularity":"minute"%s}`,
				test.queryType, testInterval, test.extra)
			r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
			if err != nil {
				t.Fatal(err)
			}
			plan := trq.ParsedQuery.(*model.QueryPlan)
			if fmt.Sprint(plan.Dimensions()) != fmt.Sprint(test.dimensions) {
				t.Fatalf("dimensions = %v, want %v", plan.Dimensions(), test.dimensions)
			}
		})
	}
}

func TestParseTimeRangeQueryCanonicalContext(t *testing.T) {
	base := `{"queryType":"timeseries","dataSource":"wiki","intervals":["` + testInterval + `"],"granularity":"minute","filter":{"type":"selector","value":"x","dimension":"page"}`
	first := base + `,"context":{"queryId":"one","sqlQueryId":"sql-one","priority":10,"timeout":1000,"queryDeadline":2000}}`
	second := `{"granularity":"minute","intervals":["` + testInterval + `"],"filter":{"value":"x","dimension":"page","type":"selector"},"dataSource":"wiki","queryType":"timeseries"}`
	parse := func(body string) string {
		r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		trq, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
		if err != nil {
			t.Fatal(err)
		}
		return trq.Statement
	}
	if parse(first) != parse(second) {
		t.Fatal("transient context or JSON key order changed canonical identity")
	}
	semantic := base + `,"context":{"skipEmptyBuckets":true}}`
	if parse(semantic) == parse(second) {
		t.Fatal("semantic context was stripped from canonical identity")
	}
}

func TestParseTimeRangeQueryFallbacks(t *testing.T) {
	tests := []struct {
		name, method, contentType, body string
		canOPC                          bool
		reason                          analysisReason
	}{
		{"method", http.MethodGet, headers.ValueApplicationJSON, druidQuery("timeseries", `"minute"`), false, reasonUnsupportedMethod},
		{"content type", http.MethodPost, "text/plain", druidQuery("timeseries", `"minute"`), false, reasonUnsupportedContentType},
		{"invalid JSON", http.MethodPost, headers.ValueApplicationJSON, `{`, false, reasonInvalidJSON},
		{"unsupported type", http.MethodPost, headers.ValueApplicationJSON, druidQuery("scan", `"minute"`), true, reasonUnsupportedQueryType},
		{"invalid context", http.MethodPost, headers.ValueApplicationJSON, `{"queryType":"timeseries","dataSource":"wiki","intervals":["` + testInterval + `"],"granularity":"minute","context":"bad"}`, true, reasonInvalidContext},
		{"multi interval", http.MethodPost, headers.ValueApplicationJSON, strings.Replace(druidQuery("timeseries", `"minute"`), `[`+strconvQuote(testInterval)+`]`, `[`+strconvQuote(testInterval)+`,"2024-02-01/2024-02-02"]`, 1), true, reasonMultipleIntervals},
		{"invalid interval", http.MethodPost, headers.ValueApplicationJSON, strings.Replace(druidQuery("timeseries", `"minute"`), testInterval, "not-an-interval", 1), true, reasonInvalidInterval},
		{"unknown granularity", http.MethodPost, headers.ValueApplicationJSON, druidQuery("timeseries", `"fortnight"`), true, reasonUnsupportedGranularity},
		{"by segment", http.MethodPost, headers.ValueApplicationJSON, strings.TrimSuffix(druidQuery("topN", `"minute"`), "}") + `,"context":{"bySegment":true}}`, true, reasonUnsupportedShape},
		{"numeric timestamps", http.MethodPost, headers.ValueApplicationJSON, strings.TrimSuffix(druidQuery("timeseries", `"minute"`), "}") + `,"context":{"serializeDateTimeAsLong":true}}`, true, reasonUnsupportedShape},
		{"timeseries grand total", http.MethodPost, headers.ValueApplicationJSON, strings.TrimSuffix(druidQuery("timeseries", `"minute"`), "}") + `,"context":{"grandTotal":true}}`, true, reasonUnsupportedShape},
		{"groupBy array", http.MethodPost, headers.ValueApplicationJSON, strings.TrimSuffix(druidQuery("groupBy", `"minute"`), "}") + `,"context":{"resultAsArray":true}}`, true, reasonUnsupportedShape},
		{"topN missing dimension", http.MethodPost, headers.ValueApplicationJSON, druidQuery("topN", `"minute"`), true, reasonUnsupportedDimension},
		{"invalid dimensions", http.MethodPost, headers.ValueApplicationJSON, strings.TrimSuffix(druidQuery("groupBy", `"minute"`), "}") + `,"dimensions":[{"type":"unknown"}]}`, true, reasonUnsupportedDimension},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "http://trickster/druid/v2", strings.NewReader(test.body))
			r.Header.Set(headers.NameContentType, test.contentType)
			trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
			if err == nil || canOPC != test.canOPC || reasonOf(t, err) != test.reason {
				t.Fatalf("got trq=%v canOPC=%t err=%v", trq, canOPC, err)
			}
			if canOPC && (trq == nil || string(trq.OriginalBody) != test.body) {
				t.Fatal("OPC fallback did not preserve its request")
			}
		})
	}
}

func TestParseTimeRangeQueryConvertsHalfOpenExtent(t *testing.T) {
	body := `{"queryType":"timeseries","dataSource":"wiki","intervals":["2024-01-01T00:00:00Z/2024-01-01T00:03:00Z"],"granularity":"minute"}`
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	trq, _, _, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := wantStart.Add(2 * time.Minute)
	if !trq.Extent.Start.Equal(wantStart) || !trq.Extent.End.Equal(wantEnd) {
		t.Fatalf("extent = %s, want [%s, %s]", trq.Extent.String(), wantStart, wantEnd)
	}
}
