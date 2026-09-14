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
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestParseTimeRangeQueryAdditionalInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		r      *http.Request
		reason analysisReason
	}{
		{"nil request", nil, reasonUnsupportedMethod},
		{"empty body", jsonRequest(" "), reasonInvalidJSON},
		{"body read", func() *http.Request {
			r := jsonRequest("")
			r.Body = io.NopCloser(failingReader{})
			return r
		}(), reasonInvalidJSON},
		{"trailing JSON", jsonRequest(`{} {}`), reasonInvalidJSON},
		{"empty intervals", jsonRequest(`{"queryType":"timeseries","dataSource":"wiki","intervals":[],"granularity":"minute"}`), reasonInvalidInterval},
		{"non-string interval", jsonRequest(`{"queryType":"timeseries","dataSource":"wiki","intervals":[1],"granularity":"minute"}`), reasonInvalidInterval},
		{"bad interval end", jsonRequest(`{"queryType":"timeseries","dataSource":"wiki","intervals":["2024-01-01/bad"],"granularity":"minute"}`), reasonInvalidInterval},
		{"interval before nanosecond epoch", jsonRequest(`{"queryType":"timeseries","dataSource":"wiki","intervals":["1600-01-01/1600-01-02"],"granularity":"minute"}`), reasonInvalidInterval},
		{"duplicate dimensions", jsonRequest(`{"queryType":"groupBy","dataSource":"wiki","intervals":["2024-01-01/2024-01-02"],"granularity":"minute","dimensions":["country","country"]}`), reasonUnsupportedDimension},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := (&Client{}).ParseTimeRangeQuery(test.r)
			if err == nil || reasonOf(t, err) != test.reason {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func jsonRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	return r
}

func TestParseHelpers(t *testing.T) {
	for _, value := range []string{"2024-01-01", "2024-01-01T12:34", "2024-01-01T12:34:56.5"} {
		if _, err := parseDruidTime(value); err != nil {
			t.Errorf("parseDruidTime(%q): %v", value, err)
		}
	}
	if _, _, err := parseInterval("bad/2024-01-01"); err == nil {
		t.Fatal("expected bad interval start error")
	}
	if _, _, _, ok := parseGranularity(nil); ok {
		t.Fatal("nil granularity was accepted")
	}
	if _, _, _, ok := parseGranularity(map[string]any{"type": "duration", "duration": "bad"}); ok {
		t.Fatal("bad duration was accepted")
	}
	if _, _, _, ok := parseGranularity(map[string]any{"type": "duration", "duration": "60000", "origin": 1}); ok {
		t.Fatal("bad duration origin was accepted")
	}
	if _, _, _, ok := parseGranularity(map[string]any{"type": "duration", "duration": "60000", "origin": "1600-01-01"}); ok {
		t.Fatal("out-of-range duration origin was accepted")
	}
	if _, _, _, ok := parseGranularity(map[string]any{"type": "period", "period": "bad"}); ok {
		t.Fatal("bad period was accepted")
	}
	if _, ok := integerValue(float64(1)); ok {
		t.Fatal("float integer value was accepted")
	}
	if _, _, _, ok := fixedPeriod("PT1Sx"); ok {
		t.Fatal("bad fixed period was accepted")
	}
	if _, _, _, ok := fixedPeriod("P999999999999999999999D"); ok {
		t.Fatal("overflowing fixed period was accepted")
	}
	if _, _, _, _, ok := parseDatePeriod("1D1W"); ok {
		t.Fatal("out-of-order date period was accepted")
	}
	if _, _, _, ok := parseTimePeriod("1M1H"); ok {
		t.Fatal("out-of-order time period was accepted")
	}
	if _, ok := composePeriodDuration(math.MaxInt64, 0, 0, 0, 0); ok {
		t.Fatal("overflowing duration was accepted")
	}
	if truncateToPhase(time.Unix(-1, 500_000_000), time.Second, 0).UnixNano() != -1_000_000_000 {
		t.Fatal("negative timestamp was not floored")
	}
	for _, zone := range []string{"UTC", "Etc/UTC", "GMT", "Etc/GMT"} {
		if !isUTCZone(zone) {
			t.Errorf("UTC zone %q was rejected", zone)
		}
	}
}

func TestDimensionAndCanonicalHelpers(t *testing.T) {
	tests := []struct {
		value any
		want  string
		ok    bool
	}{
		{"country", "country", true},
		{map[string]any{"dimension": "device"}, "device", true},
		{map[string]any{"outputName": 1}, "", false},
		{map[string]any{"delegate": map[string]any{"dimension": "page"}}, "page", true},
		{1, "", false},
	}
	for _, test := range tests {
		got, ok := dimensionName(test.value)
		if got != test.want || ok != test.ok {
			t.Errorf("dimensionName(%v) = (%q, %t)", test.value, got, ok)
		}
	}
	if canonicalValue(map[string]any{"b": 2, "a": 1}) != `{"a":1,"b":2}` {
		t.Fatal("object datasource was not canonicalized")
	}
	if booleanValue("true") != true || booleanValue(1) {
		t.Fatal("boolean coercion failed")
	}
	if _, _, _, err := marshalJSONObject(map[string]any{"bad": make(chan int)}, nil); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestBackfillToleranceSources(t *testing.T) {
	if druidBackfillTolerance(nil) != time.Minute {
		t.Fatal("nil request did not use Druid default")
	}
	r := request.SetResources(jsonRequest(`{}`), &request.Resources{})
	if druidBackfillTolerance(r) != time.Minute {
		t.Fatal("nil backend options did not use Druid default")
	}
	r = request.SetResources(jsonRequest(`{}`), &request.Resources{
		BackendOptions: &bo.Options{BackfillTolerance: 2 * 60 * 1_000_000_000},
	})
	if druidBackfillTolerance(r) != 2*time.Minute {
		t.Fatal("configured backfill tolerance was ignored")
	}
}

func TestClassifiedErrorUnwrap(t *testing.T) {
	err := newClassifiedError(errObjectCache, reasonUnsupportedQueryType)
	if !errors.Is(err, errObjectCache) {
		t.Fatal("classified error does not unwrap")
	}
}
