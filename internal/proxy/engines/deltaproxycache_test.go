/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package engines

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
	tu "github.com/Comcast/trickster/internal/util/testing"
	"github.com/Comcast/trickster/pkg/promsim"
)

// test queries
const (
	queryReturnsOKNoLatency = "some_query_here{latency_ms=0,range_latency_ms=0}"
	queryReturnsBadPayload  = "some_query_here{invalid_response_body=1,latency_ms=0,range_latency_ms=0}"
	queryReturnsBadRequest  = "some_query_here{status_code=400,latency_ms=0,range_latency_ms=0}"
	queryReturnsBadGateway  = "some_query_here{status_code=502,latency_ms=0,range_latency_ms=0}"
)

func setupTestServer() (*httptest.Server, *config.OriginConfig, *PromTestClient, error) {

	// set things up

	// simuated prometheus timeseries database server
	es := promsim.NewTestServer()

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "--origin-type", "prometheus", "--log-level", "debug"})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		return nil, nil, nil, err
	}

	cfg := config.Origins["default"]
	client := &PromTestClient{config: cfg, cache: cache, webClient: tu.NewTestWebClient()}
	client.RegisterRoutes("default", cfg)

	p, ok := cfg.Paths["/api/v1/query_range"]
	if !ok {
		return nil, nil, nil, fmt.Errorf("could not find path %s", "/api/v1/query_range")
	}
	p.CacheKeyParams = []string{"rangeKey"}

	p, ok = cfg.Paths["/api/v1/query"]
	if !ok {
		return nil, nil, nil, fmt.Errorf("could not find path %s", "/api/v1/query")
	}
	p.CacheKeyParams = []string{"instantKey"}

	return es, cfg, client, nil
}

func TestDeltaProxyCacheRequestMissThenHit(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// get cache hit coverage too by repeating:

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "hit"})
	if err != nil {
		t.Error(err)
	}
}

func TestDeltaProxyCacheRequestAllItemsTooNew(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	cfg.BackfillToleranceSecs = 600
	cfg.BackfillTolerance = time.Second * time.Duration(cfg.BackfillToleranceSecs)

	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second
	end := time.Now()

	extr := timeseries.Extent{Start: end.Add(-time.Duration(5) * time.Minute), End: end}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extr.Start, extr.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	if resp.Header.Get("status") != "" {
		t.Errorf("status header should not be present. Found with value %s", resp.Header.Get("stattus"))
	}

	// ensure the request was sent through the proxy instead of the DeltaProxyCache
	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestRemoveStale(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true

	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second
	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// get cache hit coverage too by repeating:

	cfg.TimeseriesRetention = 10

	extr = timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: now}
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestMarshalFailure(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	client.RangeCacheKey = "failkey"

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func normalizeTime(t time.Time, d time.Duration) time.Time {
	return time.Unix((t.Unix()/int64(d.Seconds()))*int64(d.Seconds()), 0)
	//return t.Truncate(d)
}

func TestDeltaProxyCacheRequestPartialHit(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	client.RangeCacheKey = "test-range-key"
	client.InstantCacheKey = "test-instant-key"

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: normalizeTime(extr.Start, step), End: normalizeTime(extr.End, step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// test partial hit (needing an upper fragment)
	phitStart := normalizeTime(extr.End.Add(step), step)
	extr.End = extr.End.Add(time.Duration(1) * time.Hour) // Extend the top by 1 hour to generate partial hit
	extn.End = normalizeTime(extr.End, step)

	expectedFetched := fmt.Sprintf("[%d:%d]", phitStart.Unix(), extn.End.Unix())
	expected, _, _ = promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

	// test partial hit (needing a lower fragment)
	phitEnd := extn.Start.Add(-step)
	extr.Start = extr.Start.Add(time.Duration(-1) * time.Hour)
	extn.Start = normalizeTime(extr.Start, step)

	expectedFetched = fmt.Sprintf("[%d:%d]", extn.Start.Unix(), phitEnd.Unix())
	expected, _, _ = promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

	// test partial hit (needing both upper and lower fragments)
	phitEnd = normalizeTime(extr.Start.Add(-step), step)
	phitStart = normalizeTime(extr.End.Add(step), step)

	extr.Start = extr.Start.Add(time.Duration(-1) * time.Hour)
	extn.Start = normalizeTime(extr.Start, step)
	extr.End = extr.End.Add(time.Duration(1) * time.Hour) // Extend the top by 1 hour to generate partial hit
	extn.End = normalizeTime(extr.End, step)

	expectedFetched = fmt.Sprintf("[%d:%d,%d:%d]",
		extn.Start.Unix(), phitEnd.Unix(), phitStart.Unix(), extn.End.Unix())

	expected, _, _ = promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

	// test partial hit returns an error
	extr.Start = extr.Start.Add(time.Duration(-1) * time.Hour)
	extn.Start = normalizeTime(extr.Start, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadPayload)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltayProxyCacheRequestDeltaFetchError(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	client.RangeCacheKey = "testkey"

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: normalizeTime(extr.Start, step), End: normalizeTime(extr.End, step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// test partial hit (needing an upper fragment)
	phitStart := extr.End.Add(step)
	extr.End = extr.End.Add(time.Duration(1) * time.Hour) // Extend the top by 1 hour to generate partial hit
	extn.End = extr.End.Truncate(step)

	expectedFetched := fmt.Sprintf("[%d:%d]", phitStart.Truncate(step).Unix(), extn.End.Unix())
	promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	// Switch to the failed query.
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadGateway)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestRangeMiss(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(3600) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// Test Range Miss Low End

	extr.Start = extr.Start.Add(time.Duration(-3) * time.Hour)
	extn.Start = extr.Start.Truncate(step)
	extr.End = extr.Start.Add(time.Duration(1) * time.Hour)
	extn.End = extr.End.Truncate(step)

	expectedFetched := fmt.Sprintf("[%d:%d]", extn.Start.Unix(), extn.End.Unix())
	expected, _, _ = promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "rmiss"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

	// Test Range Miss High End

	extr.Start = now.Add(time.Duration(-10) * time.Hour)
	extn.Start = extr.Start.Truncate(step)
	extr.End = now.Add(time.Duration(-8) * time.Hour)
	extn.End = extr.End.Truncate(step)

	expectedFetched = fmt.Sprintf("[%d:%d", extn.Start.Unix(), extn.End.Unix())
	expected, _, _ = promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "rmiss"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}
}

func TestDeltaProxyCacheRequestFastForward(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	client.InstantCacheKey = "test-dpc-ff-key-instant"
	client.RangeCacheKey = "test-dpc-ff-key-range"

	cfg.FastForwardDisable = false
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	client.fftime = now.Truncate(client.cache.Configuration().FastForwardTTL)

	extr := timeseries.Extent{Start: now.Add(-time.Duration(12) * time.Hour), End: now}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
		client.InstantCacheKey, client.RangeCacheKey, int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	expectedMatrix, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	em, err := client.UnmarshalTimeseries([]byte(expectedMatrix))
	if err != nil {
		t.Error(err)
	}
	em.SetExtents(timeseries.ExtentList{extn})

	expectedVector, _, _ := promsim.GetInstantData(queryReturnsOKNoLatency, client.fftime)
	ev, err := client.UnmarshalInstantaneous([]byte(expectedVector))
	if err != nil {
		t.Error(err)
	}
	ev.SetStep(step)

	if len(ev.Extents()) == 1 && len(em.Extents()) > 0 && ev.Extents()[0].Start.Truncate(time.Second).After(em.Extents()[0].End) {
		em.Merge(false, ev)
	}

	em.SetExtents(nil)
	b, err := client.MarshalTimeseries(em)
	if err != nil {
		t.Error(err)
	}

	expected := string(b)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "miss"})
	if err != nil {
		t.Error(err)
	}

	// do it again and look for a cache hit on the timeseries and fast forward

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "hit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "hit"})
	if err != nil {
		t.Error(err)
	}

	instantKey := cfg.Host + "." + md5.Checksum(strings.Replace(u.Path, "_range", "", -1)+client.InstantCacheKey) + ".sz"
	client.cache.Remove(instantKey)

	u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
		client.InstantCacheKey, client.RangeCacheKey, int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadPayload)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "err"})
	if err != nil {
		t.Error(err)
	}

	// Now test a Response Code error

	client.cache.Remove(instantKey)

	u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
		client.InstantCacheKey, client.RangeCacheKey, int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "err"})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestFastForwardUrlError(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("throw_ffurl_error=1&step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	cfg.FastForwardDisable = false
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "err"})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestWithRefresh(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	cfg.IgnoreCachingHeaders = false

	r := httptest.NewRequest("GET", es.URL, nil)
	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "purge"})
	if err != nil {
		t.Error(err)
	}
}

func TestDeltaProxyCacheRequestWithRefreshError(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	cfg.IgnoreCachingHeaders = false

	r := httptest.NewRequest("GET", es.URL, nil)
	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestWithUnmarshalAndUpstreamErrors(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	client.RangeCacheKey = "testkey"

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	key := cfg.Host + ".546ecac4cc8b7ed423920fa7ebd5f230.sz"

	_, err = client.cache.Retrieve(key, false)
	if err != nil {
		t.Error(err)
	}

	client.cache.Store(key, []byte("foo"), time.Duration(30)*time.Second)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)
	client.cache.Store(key, []byte("foo"), time.Duration(30)*time.Second)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequest_BadParams(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	const query = "some_query_here{}"
	step := time.Duration(300) * time.Second
	end := time.Now()
	start := end.Add(-time.Duration(6) * time.Hour)

	u := r.URL
	u.Path = "/api/v1/query_range"
	// Intentional typo &q instead of &query to force a proxied request due to ParseTimeRangeQuery() error
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&q=%s", int(step.Seconds()), start.Unix(), end.Unix(), query)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

	// ensure the request was sent through the proxy instead of the DeltaProxyCache
	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestCacheMissUnmarshalFailed(t *testing.T) {
	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadPayload)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	_, err = client.UnmarshalTimeseries(body)
	if err == nil {
		t.Errorf("expected unmarshaling error for %s", string(body))
	}

}

func TestDeltaProxyCacheRequestOutOfWindow(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	query := "some_query_here{}"
	step := time.Duration(300) * time.Second
	// Times are out-of-window for being cacheable
	start := time.Unix(0, 0)
	end := time.Unix(1800, 0)

	// we still expect the same results
	expected, _, _ := promsim.GetTimeSeriesData(query, start, end, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), start.Unix(), end.Unix(), query)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	// Fully Out-of-Window Requests should be proxied and not cached
	testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})

	// do it again to ensure another cache miss
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	// Fully Out-of-Window Requests should be proxied and not cached
	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestDeltaProxyCacheRequestBadGateway(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = true
	cfg.IgnoreCachingHeaders = false

	r := httptest.NewRequest("GET", es.URL, nil)
	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadGateway)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadGateway)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequest_BackfillTolerance(t *testing.T) {

	es := promsim.NewTestServer()
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "--origin-type", "prometheus", "--log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"], cache: cache, webClient: tu.NewTestWebClient()}

	cfg := client.Configuration()
	cfg.BackfillTolerance = time.Duration(300) * time.Second
	cfg.FastForwardDisable = true

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	query := "some_query_here{}"
	step := time.Duration(300) * time.Second

	now := time.Now()
	x := timeseries.Extent{Start: now.Add(-time.Duration(6) * time.Hour), End: now}
	xn := timeseries.Extent{Start: now.Add(-time.Duration(6) * time.Hour).Truncate(step), End: now.Truncate(step)}

	// We can predict what slice will need to be fetched and ensure that is only what is requested upstream
	expectedFetched := fmt.Sprintf("[%d:%d]", xn.End.Unix(), xn.End.Unix())
	expected, _, _ := promsim.GetTimeSeriesData(query, xn.Start, xn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), x.Start.Unix(), x.End.Unix(), query)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// get cache partial hit coverage too by repeating:
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "phit"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestFFTTLBiggerThanStep(t *testing.T) {

	es, cfg, client, err := setupTestServer()
	if err != nil {
		t.Error(err)
	}
	defer es.Close()

	cfg.FastForwardDisable = false
	r := httptest.NewRequest("GET", es.URL, nil)

	step := time.Duration(300) * time.Second
	client.cache.Configuration().FastForwardTTL = step + 1

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"ffstatus": "off"})
	if err != nil {
		t.Error(err)
	}

}
