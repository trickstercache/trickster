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

package engines

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/promsim"
	"github.com/trickstercache/trickster/v2/pkg/testutil/stepwindow"
	tst "github.com/trickstercache/trickster/v2/pkg/testutil/timeseries/model"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	engineDPC = "DeltaProxyCache"
	engineOPC = "ObjectProxyCache"
	offStep   = 300 * time.Second
)

func offHarness(t *testing.T, mode timeseries.StepAlignment) (*request.Resources,
	func(start, end time.Time, override timeseries.StepAlignment) dpcResponse,
) {
	t.Helper()
	// serves query_range requests against promsim; mode is the backend's configured step alignment
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestHarness(ts, r) })
	client := rsc.BackendClient.(*TestClient)
	client.stepAlignments = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate
	client.stepAlignment = timeseries.StepAlignmentTruncate
	o := rsc.BackendOptions
	o.FastForwardDisable, o.StepAlignment = true, mode
	// the path keys only this parameter, so a delta proxy cache key never holds the range
	key := strings.ReplaceAll(t.Name(), "/", "-")
	serve := func(start, end time.Time, override timeseries.StepAlignment) dpcResponse {
		r.URL.Path = "/prometheus/api/v1/query_range"
		r.URL.RawQuery = fmt.Sprintf("instantKey=%s&step=%d&start=%d&end=%d&query=%s", key,
			int(offStep.Seconds()), start.Unix(), end.Unix(), url.QueryEscape(queryReturnsOKNoLatency))
		req := r
		if override != 0 {
			req = r.WithContext(tctx.WithStepAlignment(r.Context(), override))
		}
		return serveDPC(client, req)
	}
	return rsc, serve
}

func offRange() (time.Time, time.Time) {
	// a past range whose end is off the grid, so the origin's own points are off the grid too
	end := time.Now().Add(-12 * time.Hour).Truncate(offStep).Add(17 * time.Second)
	return end.Add(-6 * time.Hour), end
}

func requireResult(t *testing.T, resp dpcResponse, engine, lookup string) {
	t.Helper()
	if err := testResultHeaderPartMatch(resp.header,
		map[string]string{keys.Engine: engine, keys.Status: lookup}); err != nil {
		t.Fatal(err)
	}
}

func TestDeltaProxyCacheOffKeysTheObjectCacheOnTheRawRange(t *testing.T) {
	rsc, serve := offHarness(t, timeseries.StepAlignmentOff)
	start, end := offRange()
	expected, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, start, end, offStep)

	resp := serve(start, end, 0)
	requireResult(t, resp, engineOPC, status.StatusKeyMiss)
	if err := testStringMatch(resp.body, expected); err != nil {
		t.Errorf("the response should be the origin's own points for the raw range: %v", err)
	}
	if rsc.AlternateCacheTTL != timeseries.StepAlignmentOffTTL {
		t.Errorf("object TTL = %s, want %s", rsc.AlternateCacheTTL, timeseries.StepAlignmentOffTTL)
	}
	t.Run("an identical range collapses onto one entry", func(t *testing.T) {
		resp := serve(start, end, 0)
		requireResult(t, resp, engineOPC, status.StatusHit)
		if err := testStringMatch(resp.body, expected); err != nil {
			t.Error(err)
		}
	})
	t.Run("a range one second later has its own entry", func(t *testing.T) {
		later, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, start, end.Add(time.Second), offStep)
		resp := serve(start, end.Add(time.Second), 0)
		requireResult(t, resp, engineOPC, status.StatusKeyMiss)
		if err := testStringMatch(resp.body, later); err != nil {
			t.Error(err)
		}
	})
	t.Run("so does a start one second later", func(t *testing.T) {
		requireResult(t, serve(start.Add(time.Second), end, 0), engineOPC, status.StatusKeyMiss)
	})
}

func TestDeltaProxyCacheOffWithCacheChunking(t *testing.T) {
	rsc, serve := offHarness(t, timeseries.StepAlignmentOff)
	rsc.CacheConfig.UseCacheChunking = true
	start, end := offRange()
	expected, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, start, end, offStep)
	for _, lookup := range []string{status.StatusKeyMiss, status.StatusHit} {
		resp := serve(start, end, 0)
		requireResult(t, resp, engineOPC, lookup)
		if err := testStringMatch(resp.body, expected); err != nil {
			t.Errorf("%s: %v", lookup, err)
		}
	}
}

func TestDeltaProxyCacheOffNeverReadsOrWritesTheDeltaEntry(t *testing.T) {
	_, serve := offHarness(t, 0)
	start, end := offRange()
	requireResult(t, serve(start, end, 0), engineDPC, status.StatusKeyMiss)
	requireResult(t, serve(start, end, 0), engineDPC, status.StatusHit)
	// the cached interior would answer this range in full, but off asks the origin
	resp := serve(start, end, timeseries.StepAlignmentOff)
	requireResult(t, resp, engineOPC, status.StatusKeyMiss)
	expected, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, start, end, offStep)
	if err := testStringMatch(resp.body, expected); err != nil {
		t.Error(err)
	}
	requireResult(t, serve(start, end, timeseries.StepAlignmentOff), engineOPC, status.StatusHit)
	requireResult(t, serve(start, end, 0), engineDPC, status.StatusHit)
}

func TestDeltaProxyCacheOffAppliesTransformations(t *testing.T) {
	injected := dataset.Tags{"injected": "yes"}
	transform := func(ts timeseries.Timeseries) { ts.(*dataset.DataSet).InjectTags(injected) }
	start, end := offRange()

	t.Run("the response carries the transformation", func(t *testing.T) {
		rsc, serve := offHarness(t, timeseries.StepAlignmentOff)
		rsc.TSTransformer = transform
		for _, lookup := range []string{status.StatusKeyMiss, status.StatusHit} {
			resp := serve(start, end, 0)
			requireResult(t, resp, engineOPC, lookup)
			if resp.code != http.StatusOK || !strings.Contains(resp.body, `"injected":"yes"`) {
				t.Errorf("%s: the transformation is missing: %d %s", lookup, resp.code, resp.body)
			}
			if resp.header.Get(headers.NameContentLength) != "" {
				t.Errorf("%s: a remarshaled body can't keep the origin's length", lookup)
			}
		}
	})
	t.Run("a merge member hands over the transformed series", func(t *testing.T) {
		rsc, serve := offHarness(t, timeseries.StepAlignmentOff)
		rsc.TSTransformer, rsc.IsMergeMember = transform, true
		resp := serve(start, end, 0)
		if resp.body != "" {
			t.Errorf("a merge member's response is merged, not written: %s", resp.body)
		}
		ds, ok := rsc.TS.(*dataset.DataSet)
		if !ok || len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 ||
			ds.Results[0].SeriesList[0].Header.Tags["injected"] != "yes" {
			t.Errorf("expected the transformed series, got %v", rsc.TS)
		}
		if rsc.Response == nil || rsc.Response.StatusCode != http.StatusOK {
			t.Errorf("expected the origin's response, got %v", rsc.Response)
		}
	})
	t.Run("an origin error is relayed as sent", func(t *testing.T) {
		rsc, serve := offHarness(t, timeseries.StepAlignmentOff)
		rsc.TSTransformer = transform
		// promsim rejects a range that ends before it starts
		resp := serve(end, start, 0)
		if resp.code != http.StatusBadRequest || strings.Contains(resp.body, "injected") {
			t.Errorf("expected the origin's 400, got %d %s", resp.code, resp.body)
		}
	})
}

func TestServeUnalignedRelaysABodyItCannotModel(t *testing.T) {
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)
	rsc.TSTransformer = func(timeseries.Timeseries) { t.Error("a body that can't be modeled was transformed") }
	trq := &timeseries.TimeRangeQuery{}
	rsc.TimeRangeQuery = trq
	w := httptest.NewRecorder()
	serveUnaligned(w, r, rsc, trq, nil, tst.Modeler(), timeseries.StepAlignmentOffTTL)
	if w.Code != http.StatusOK || w.Body.String() != "test" {
		t.Errorf("expected the origin's body as sent, got %d %q", w.Code, w.Body.String())
	}
}

func TestServeUnalignedSendsTheClientsBody(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set(headers.NameCacheControl, "no-store")
		w.Write(b)
	}))
	defer echo.Close()
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)
	send := func(original string) dpcResponse {
		// the parser left its range-free statement in the body, as ClickHouse's does
		req := httptest.NewRequest(http.MethodPost, echo.URL+"/opc", strings.NewReader("tokenized"))
		req.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		req = req.WithContext(r.Context())
		trq := &timeseries.TimeRangeQuery{OriginalBody: []byte(original), CacheKeyElements: map[string]string{
			"query": "tokenized",
		}}
		rsc.TimeRangeQuery = trq
		w := httptest.NewRecorder()
		serveUnaligned(w, req, rsc, trq, nil, nil, timeseries.StepAlignmentOffTTL)
		return dpcResponse{code: w.Code, body: w.Body.String(), header: w.Header()}
	}
	first, second := `{"range":[1,2]}`, `{"range":[1,3]}`
	for _, test := range []struct{ body, lookup string }{
		{first, status.StatusKeyMiss}, {second, status.StatusKeyMiss}, {first, status.StatusHit},
	} {
		resp := send(test.body)
		requireResult(t, resp, engineOPC, test.lookup)
		if resp.body != test.body {
			t.Errorf("got %q, want the client's body %q", resp.body, test.body)
		}
	}
}

func TestSetResponseFormat(t *testing.T) {
	const contentType, encoding = "text/csv", "gzip"
	if got := setResponseFormat(nil, nil); got != nil {
		t.Errorf("no options leave the header alone, got %v", got)
	}
	if got := setResponseFormat(nil, &timeseries.RequestOptions{}); got != nil {
		t.Errorf("empty options leave the header alone, got %v", got)
	}
	got := setResponseFormat(nil, &timeseries.RequestOptions{
		ResponseContentType: contentType, ResponseContentEncoding: encoding,
	})
	if got.Get(headers.NameContentType) != contentType || got.Get(headers.NameContentEncoding) != encoding {
		t.Errorf("got %v", got)
	}
	rh := http.Header{headers.NameContentType: {"application/json"}}
	if got := setResponseFormat(rh, &timeseries.RequestOptions{ResponseContentEncoding: encoding}); got.Get(
		headers.NameContentType) != "application/json" || got.Get(headers.NameContentEncoding) != encoding {
		t.Errorf("only the named field changes, got %v", got)
	}
}

func TestUnalignedKeyElements(t *testing.T) {
	pc := &po.Options{
		RequestParams:          map[string]string{"tenant": "a", "-debug": ""},
		CacheKeyParamsExcluded: []string{"query_id"},
	}
	qp := url.Values{
		"query": {"up"}, "start": {"1"}, "end": {"2"}, "tenant": {"b"}, "debug": {"1"},
		"query_id": {"x"}, "match[]": {"a", "b"}, "body": {"shadow"},
	}
	got := unalignedKeyElements(qp, []byte("payload"), true, pc)
	want := map[string]string{
		"param:query": "up", "param:start": "1", "param:end": "2", "param:match[]": "a&b",
		"param:body": "shadow", "body": "payload",
	}
	if len(got) != len(want) {
		t.Errorf("got %q, want %q", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got := unalignedKeyElements(qp, []byte("payload"), false, nil); len(got) != len(qp) {
		t.Errorf("without a body or a path, every parameter is keyed: %q", got)
	}
	repeated := unalignedKeyElements(url.Values{"k": {"a", "b"}}, nil, false, nil)
	for _, value := range []string{"a\x00b", "a&b", "a%26b"} {
		joined := unalignedKeyElements(url.Values{"k": {value}}, nil, false, nil)
		if repeated["param:k"] == joined["param:k"] {
			t.Errorf("repeated values and the one value %q share %q", value, joined["param:k"])
		}
	}
}

func TestServeUnalignedKeysRepeatedValuesApartFromOneValue(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headers.NameCacheControl, "no-store")
		w.Write([]byte(r.URL.RawQuery))
	}))
	defer echo.Close()
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)
	for _, test := range []struct{ query, lookup string }{
		{"k=a&k=b", status.StatusKeyMiss},
		{"k=a%00b", status.StatusKeyMiss},
		{"k=a%26b", status.StatusKeyMiss},
		{"k=a&k=b", status.StatusHit},
	} {
		req := httptest.NewRequest(http.MethodGet, echo.URL+"/opc?"+test.query, nil).WithContext(r.Context())
		trq := &timeseries.TimeRangeQuery{}
		rsc.TimeRangeQuery = trq
		w := httptest.NewRecorder()
		serveUnaligned(w, req, rsc, trq, nil, nil, timeseries.StepAlignmentOffTTL)
		resp := dpcResponse{code: w.Code, body: w.Body.String(), header: w.Header()}
		requireResult(t, resp, engineOPC, test.lookup)
		if resp.body != test.query {
			t.Errorf("got %q, want the origin's answer to %q", resp.body, test.query)
		}
	}
}

func TestDeltaProxyCacheStepAlignmentGatesFastForward(t *testing.T) {
	const (
		supported = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
			timeseries.StepAlignmentPartialEnd
		step = 300 * time.Second
	)
	const (
		stepDirective = " # trickster-step-align:partial_end"
		ffDirective   = " # trickster-fast-forward:off"
	)
	tests := []struct {
		name                          string
		configured, override, allowed timeseries.StepAlignment
		directive                     string
		ffDisable                     bool
		ffStatus                      string
		fallback                      bool
	}{
		{name: "the default partial_end runs fast forward", ffStatus: status.StatusKeyMiss},
		{
			name: "a configured truncate skips fast forward", configured: timeseries.StepAlignmentTruncate,
			ffStatus: statusOff,
		},
		{
			name: "an override wins over the configured mode", configured: timeseries.StepAlignmentTruncate,
			override: timeseries.StepAlignmentPartialEnd, ffStatus: status.StatusKeyMiss,
		},
		{
			name: "an unsupported override keeps the default", override: timeseries.StepAlignmentDrop,
			ffStatus: status.StatusKeyMiss, fallback: true,
		},
		{
			name: "a directive wins over the configured mode", configured: timeseries.StepAlignmentTruncate,
			directive: stepDirective, ffStatus: status.StatusKeyMiss,
		},
		{
			name: "an unsupported directive keeps the default", directive: " # trickster-step-align:drop",
			ffStatus: status.StatusKeyMiss, fallback: true,
		},
		{name: "the legacy directive skips fast forward", directive: ffDirective, ffStatus: statusOff},
		{
			name: "a step alignment directive outranks the legacy one", directive: ffDirective + stepDirective,
			configured: timeseries.StepAlignmentTruncate, ffStatus: status.StatusKeyMiss,
		},
		{
			name: "fast_forward_disable yields to a directive", ffDisable: true,
			configured: timeseries.StepAlignmentTruncate, directive: stepDirective, ffStatus: status.StatusKeyMiss,
		},
		{
			name: "an ALB's mode wins over a directive a member lacks", override: timeseries.StepAlignmentTruncate,
			directive: stepDirective, ffStatus: statusOff,
		},
		{
			name: "a directive every ALB member supports wins over its mode", override: timeseries.StepAlignmentTruncate,
			allowed: supported, directive: stepDirective, ffStatus: status.StatusKeyMiss,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts, _, r, rsc, err := setupTestHarnessDPC()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestHarness(ts, r)
			client := rsc.BackendClient.(*TestClient)
			client.stepAlignments, client.stepAlignment = supported, timeseries.StepAlignmentPartialEnd
			o := rsc.BackendOptions
			o.FastForwardDisable, o.StepAlignment = test.ffDisable, test.configured
			fallbacks := metrics.StepAlignmentFallbacks.WithLabelValues(o.Name,
				timeseries.StepAlignmentNameDrop, timeseries.StepAlignmentNamePartialEnd)
			before := testutil.ToFloat64(fallbacks)

			// fast forward needs the request to end at the engine's step-aligned now
			resp, ok := stepwindow.Retry(step, 3, func(attempt int, now time.Time) dpcResponse {
				client.InstantCacheKey = fmt.Sprintf("test-dpc-align-%d-%d-instant", i, attempt)
				client.RangeCacheKey = fmt.Sprintf("test-dpc-align-%d-%d-range", i, attempt)
				client.fftime = now.Truncate(time.Duration(o.PartialBucketTTL))
				u := r.URL
				u.Path = "/prometheus/api/v1/query_range"
				u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
					client.InstantCacheKey, client.RangeCacheKey, int(step.Seconds()),
					now.Add(-time.Hour).Unix(), now.Unix(), url.QueryEscape(queryReturnsOKNoLatency+test.directive))
				req := r
				if test.override != 0 {
					req = r.WithContext(tctx.WithStepAlignmentOverride(r.Context(), &tctx.StepAlignmentOverride{
						Mode: test.override, Allowed: test.override | test.allowed,
					}))
				}
				return serveDPC(client, req)
			})
			if !ok {
				t.Fatal("every attempt straddled a step boundary")
			}
			if err := testResultHeaderPartMatch(resp.header,
				map[string]string{keys.FFStatus: test.ffStatus}); err != nil {
				t.Error(err)
			}
			if got := testutil.ToFloat64(fallbacks) - before; (got > 0) != test.fallback {
				t.Errorf("fallbacks counted: %v, want a fallback: %v", got, test.fallback)
			}
		})
	}
}
