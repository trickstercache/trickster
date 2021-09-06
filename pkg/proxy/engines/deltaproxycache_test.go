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
	"testing"
	"time"

	mockprom "github.com/trickstercache/mockster/pkg/mocks/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

// test queries
const (
	queryReturnsOKNoLatency = "some_query_here{latency_ms=0,range_latency_ms=0}"
	queryReturnsBadPayload  = "some_query_here{invalid_response_body=1,latency_ms=0,range_latency_ms=0}"
	queryReturnsBadRequest  = "some_query_here{status_code=400,latency_ms=0,range_latency_ms=0}"
	queryReturnsBadGateway  = "some_query_here{status_code=502,latency_ms=0,range_latency_ms=0}"
)

var testConfigFile string

func setupTestHarnessDPC() (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *request.Resources, error) {

	client := &TestClient{}
	ts, w, r, _, err := tu.NewTestInstance(testConfigFile,
		client.DefaultPathConfigs, 200, "", nil, "promsim", "/prometheus/api/v1/query_range", "debug")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	rsc := request.GetResources(r)
	rsc.BackendClient = client
	rsc.Tracer = tu.NewTestTracer()
	pc := rsc.PathConfig

	if pc == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find path %s", "/prometheus/api/v1/query_range")
	}

	bo := rsc.BackendOptions
	cc := rsc.CacheClient

	b, _ := backends.NewTimeseriesBackend(bo.Name, bo, client.RegisterHandlers, nil, cc, nil)
	client.TimeseriesBackend = b
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = client.HTTPClient()

	pc.CacheKeyParams = []string{"rangeKey"}
	pc.CacheKeyParams = []string{"instantKey"}

	return ts, w, r, rsc, nil
}

func TestDeltaProxyCacheRequestMissThenHit(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	// get cache hit coverage too by repeating:

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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

func TestDeltaProxyCacheRequestRemoveStale(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second
	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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

	o.TimeseriesRetention = 10

	extr = timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: now}
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

}

// Will understand why this test is failing, and if it's due to an application or test defect,
// Will commit to test issue fix in v1.2.0 or app defect fix in the next release of v1.1.x

// func TestDeltaProxyCacheRequestRemoveStaleLRU(t *testing.T) {

// 	testConfigFile = "../../../testdata/test.cache-lru.conf"
// 	ts, w, r, rsc, err := setupTestHarnessDPC()
// 	testConfigFile = ""
// 	if err != nil {
// 		t.Error(err)
// 	}
// 	defer ts.Close()

// 	client := rsc.BackendClient.(*TestClient)
// 	o := rsc.BackendOptions
// 	rsc.CacheConfig.Provider = "test"

// 	o.FastForwardDisable = true

// 	step := time.Duration(300) * time.Second
// 	now := time.Now()
// 	end := now.Add(-time.Duration(12) * time.Hour)

// 	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
// 	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

// 	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

// 	u := r.URL
// 	u.Path = "/prometheus/api/v1/query_range"
// 	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
// 		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

// 	client.QueryRangeHandler(w, r)
// 	resp := w.Result()

// 	bodyBytes, err := io.ReadAll(resp.Body)
// 	if err != nil {
// 		t.Error(err)
// 	}

// 	err = testStringMatch(string(bodyBytes), expected)
// 	if err != nil {
// 		t.Error(err)
// 	}

// 	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
// 	if err != nil {
// 		t.Error(err)
// 	}

// 	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
// 	if err != nil {
// 		t.Error(err)
// 	}

// 	// get cache hit coverage too by repeating:

// 	o.TimeseriesRetention = 10

// 	extr = timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: now}
// 	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
// 		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

// 	w = httptest.NewRecorder()

// 	client.QueryRangeHandler(w, r)
// 	resp = w.Result()

// 	_, err = io.ReadAll(resp.Body)
// 	if err != nil {
// 		t.Error(err)
// 	}

// 	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
// 	if err != nil {
// 		t.Error(err)
// 	}

// }

func TestDeltaProxyCacheRequestMarshalFailure(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	rsc.CacheConfig.Provider = "test"
	o.CacheKeyPrefix = "test"

	cc := rsc.CacheClient
	cc.Store("test.409d551e3653f5ad5aa9acbdac8d4ac3", []byte("x"), time.Second*1)

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second
	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	client.RangeCacheKey = "test-range-key-phit"
	client.InstantCacheKey = "test-instant-key-phit"

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: normalizeTime(extr.Start, step), End: normalizeTime(extr.End, step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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

	expectedFetched := "[" + timeseries.ExtentList{timeseries.Extent{Start: phitStart, End: extn.End}}.String() + "]"
	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	r.URL = u

	time.Sleep(time.Millisecond * 10)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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

	expectedFetched = "[" + timeseries.ExtentList{timeseries.Extent{Start: extn.Start, End: phitEnd}}.String() + "]"
	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	r.URL = u

	time.Sleep(time.Millisecond * 10)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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

	expectedFetched = "[" + timeseries.ExtentList{timeseries.Extent{Start: extn.Start, End: phitEnd}}.String() + "," +
		timeseries.ExtentList{timeseries.Extent{Start: phitStart, End: extn.End}}.String() + "]"

	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	time.Sleep(time.Millisecond * 10)

	r.URL = u
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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
}

func TestDeltayProxyCacheRequestDeltaFetchError(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	client.RangeCacheKey = "testkey"
	client.InstantCacheKey = "testInstantKey"

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: normalizeTime(extr.Start, step), End: normalizeTime(extr.End, step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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
	//phitStart := extr.End.Add(step)
	extr.End = extr.End.Add(time.Duration(1) * time.Hour) // Extend the top by 1 hour to generate partial hit
	extn.End = extr.End.Truncate(step)

	//expectedFetched := fmt.Sprintf("[%d:%d]", phitStart.Truncate(step).Unix(), extn.End.Unix())
	mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	client.InstantCacheKey = "foo1"
	client.RangeCacheKey = "foo2"

	// Switch to the failed query.
	u.RawQuery = fmt.Sprintf("instantKey=foo1&step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadGateway)

	r.URL = u
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadGateway)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "proxy-error"})
	if err != nil {
		t.Error(err)
	}

	// err = testResultHeaderPartMatch(resp.Header, map[string]string{"fetched": expectedFetched})
	// if err != nil {
	// 	t.Error(err)
	// }

}

func TestDeltaProxyCacheRequestRangeMiss(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	step := time.Duration(3600) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	// Test Range Miss Low End

	extr.Start = extr.Start.Add(time.Duration(-3) * time.Hour)
	extn.Start = extr.Start.Truncate(step)
	extr.End = extr.Start.Add(time.Duration(1) * time.Hour)
	extn.End = extr.End.Truncate(step)

	expectedFetched := fmt.Sprintf("[%s]", extn.String())
	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	r.URL = u
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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

	expectedFetched = fmt.Sprintf("[%s]", extn.String())
	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)
	r.URL = u

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()
	rsc.CacheConfig.Provider = "test"

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	client.InstantCacheKey = "test-dpc-ff-key-instant"
	client.RangeCacheKey = "test-dpc-ff-key-range"

	o.FastForwardDisable = false

	step := time.Duration(300) * time.Second

	now := time.Now()
	client.fftime = now.Truncate(o.FastForwardTTL)

	extr := timeseries.Extent{Start: now.Add(-time.Duration(12) * time.Hour), End: now}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
		client.InstantCacheKey, client.RangeCacheKey,
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	modeler := client.testModeler()
	expectedMatrix, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
	em, err := modeler.WireUnmarshaler([]byte(expectedMatrix), nil)
	if err != nil {
		t.Error(err)
	}
	em.SetExtents(timeseries.ExtentList{extn})

	expectedVector, _, _ := mockprom.GetInstantData(queryReturnsOKNoLatency, client.fftime)
	ev, err := modeler.WireUnmarshaler([]byte(expectedVector), nil)
	if err != nil {
		t.Error(err)
	}
	trq := &timeseries.TimeRangeQuery{Step: step}
	ev.SetTimeRangeQuery(trq)

	if len(ev.Extents()) == 1 && len(em.Extents()) > 0 &&
		ev.Extents()[0].Start.Truncate(time.Second).After(em.Extents()[0].End) {
		em.Merge(false, ev)
	}

	em.SetExtents(nil)
	b, err := modeler.WireMarshaler(em, nil, 200)
	if err != nil {
		t.Error(err)
	}

	expected := string(b)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	// do it again and look for a cache hit on the timeseries and fast forward

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
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
}

func TestDeltaProxyCacheRequestFastForwardUrlError(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("throw_ffurl_error=1&step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	o.FastForwardDisable = false
	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequestWithUnmarshalAndUpstreamErrors(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test" // disable direct-memory and force marshaling

	client.RangeCacheKey = "testkey"

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	key := o.Host + ".dpc.61a603af5b94ea305dc3fa35af4eed98"

	cc := client.Cache()

	_, _, err = cc.Retrieve(key, false)
	if err != nil {
		t.Error(err)
	}

	cc.Store(key, []byte("foo"), time.Duration(30)*time.Second)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), expected)
	if err != nil {
		t.Error(err)
	}

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)
	cc.Store(key, []byte("foo"), time.Duration(30)*time.Second)

	r.URL = u
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequest_BadParams(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	const query = "some_query_here{}"
	step := time.Duration(300) * time.Second
	end := time.Now()
	start := end.Add(-time.Duration(6) * time.Hour)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	// Intentional typo &q instead of &query to force a proxied request due to ParseTimeRangeQuery() error
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&q=%s",
		int(step.Seconds()), start.Unix(), end.Unix(), query)

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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test" // disable direct-memory and force marshaling

	o.FastForwardDisable = true

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadRequest)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadRequest)
	if err != nil {
		t.Error(err)
	}

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadPayload)
	r.URL = u
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	modeler := client.testModeler()
	_, err = modeler.WireUnmarshaler(body, nil)
	if err == nil {
		t.Errorf("expected unmarshaling error for %s", string(body))
	}

}

func TestDeltaProxyCacheRequestOutOfWindow(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true

	query := "some_query_here{}"
	step := time.Duration(300) * time.Second
	// Times are out-of-window for being cacheable
	start := time.Unix(0, 0)
	end := time.Unix(1800, 0)

	// we still expect the same results
	expected, _, _ := mockprom.GetTimeSeriesData(query, start, end, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), start.Unix(), end.Unix(), query)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	bodyBytes, err = io.ReadAll(resp.Body)
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

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	o.FastForwardDisable = true

	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsBadGateway)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadGateway)
	if err != nil {
		t.Error(err)
	}

}

func TestDeltaProxyCacheRequest_BackfillTolerance(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.BackfillTolerance = time.Duration(300) * time.Second
	o.FastForwardDisable = true

	query := "some_query_here{}"
	step := time.Duration(300) * time.Second

	now := time.Now()
	x := timeseries.Extent{Start: now.Add(-time.Duration(6) * time.Hour), End: now}
	xn := timeseries.Extent{Start: now.Add(-time.Duration(6) * time.Hour).Truncate(step), End: now.Truncate(step)}

	// We can predict what slice will need to be fetched and ensure that is only what is requested upstream
	expected, _, _ := mockprom.GetTimeSeriesData(query, xn.Start, xn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), x.Start.Unix(), x.End.Unix(), query)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	// Give time for the object to be written to cache in a separate goroutine from response
	time.Sleep(time.Millisecond * 10)

	// get cache partial hit coverage too by repeating:
	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
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

}

func TestDeltaProxyCacheRequestFFTTLBiggerThanStep(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = false

	step := time.Duration(300) * time.Second
	o.FastForwardTTL = step + 1

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: extr.Start.Truncate(step), End: extr.End.Truncate(step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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

func TestDeltaProxyCacheRequestShardByPoints(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	rsc.CacheConfig.Provider = "test"

	client.RangeCacheKey = "test-range-key-phit"
	client.InstantCacheKey = "test-instant-key-phit"

	o.FastForwardDisable = true
	o.ShardStep = 3 * time.Hour
	o.DoesShard = true

	step := time.Duration(300) * time.Second
	now := time.Now()
	end := now.Add(-12 * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}
	extn := timeseries.Extent{Start: normalizeTime(extr.Start, step), End: normalizeTime(extr.End, step)}

	expected, _, _ := mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	client.QueryRangeHandler(w, r)
	resp := w.Result()

	bodyBytes, err := io.ReadAll(resp.Body)
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
	extr.End = extr.End.Add(time.Duration(6) * time.Hour) // Extend the top by 6 hours to generate partial hit
	extn.End = normalizeTime(extr.End, step)

	expectedFetched := "[" + timeseries.ExtentList{timeseries.Extent{Start: phitStart, End: extn.End}}.String() + "]"
	expected, _, _ = mockprom.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)

	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&rk=%s&ik=%s", int(step.Seconds()),
		extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency, client.RangeCacheKey, client.InstantCacheKey)

	r.URL = u

	time.Sleep(time.Millisecond * 10)

	w = httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	resp = w.Result()

	bodyBytes, err = io.ReadAll(resp.Body)
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

}
