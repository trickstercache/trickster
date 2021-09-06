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

package irondb

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestFetchHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "irondb", "/rollup/00112233-4455-6677-8899-aabbccddeeff/metric"+
			"?start_ts=0&end_ts=900&rollup_span=300s&type=average", "debug")
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	client.FetchHandler(w, r)
	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func TestFetchHandlerDeriveCacheKey(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	path := "/fetch/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", path, "debug")
	if err != nil {
		t.Error(err)
	}

	r.Body = io.NopCloser(bytes.NewReader([]byte("{}")))

	const expected = "a34bbb372c505e9eea0e0589e16c0914"
	var result string
	result, r.Body = client.fetchHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("expected %s got %s", expected, result)
	}

}

func TestFetchHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	//hc := tu.NewTestWebClient()
	o := bo.New()

	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)
	r, err := http.NewRequest(http.MethodGet, "http://0/", nil)
	if err != nil {
		t.Error(err)
	}

	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error")))

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300, "count": 5}`)))

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.fetchHandlerSetExtent(nil, nil, nil)
	client.fetchHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})
	client.fetchHandlerSetExtent(r, nil, &timeseries.Extent{Start: now, End: now})
	r.Body = io.NopCloser(bytes.NewReader([]byte(`{a}`)))
	client.fetchHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})

}

func TestFetchHandlerParseTimeRangeQuery(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	r, err := http.NewRequest(http.MethodGet, "http://0/", nil)
	if err != nil {
		t.Error(err)
	}

	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300, "count": 5}`)))
	_, err = client.fetchHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}

	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"period": 300, "count": 5}`)))
	expected := "missing request parameter: start"
	_, err = client.fetchHandlerParseTimeRangeQuery(r)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"start": 300, "count": 5}`)))
	expected = "missing request parameter: period"
	_, err = client.fetchHandlerParseTimeRangeQuery(r)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300}`)))
	expected = "missing request parameter: count"
	_, err = client.fetchHandlerParseTimeRangeQuery(r)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}
}
