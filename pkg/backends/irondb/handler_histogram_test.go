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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestHistogramHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		"{}", nil, "irondb", "/histogram/0/900/300/00112233-4455-6677-8899-aabbccddeeff/"+
			"metric", "debug")
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

	client.HistogramHandler(w, r)
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

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET",
		"http://0/irondb/histogram/0/900/300/"+
			"00112233-4455-6677-8899-aabbccddeeff/"+
			"metric", nil)

	r = request.SetResources(r, rsc)

	client.HistogramHandler(w, r)
	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func TestHistogramHandlerDeriveCacheKey(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	path := "/histogram/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "irondb", path, "debug")
	if err != nil {
		t.Error(err)
	}

	client := backendClient.(*Client)

	expected := "11cc1b20a869f6ff0559b08b014c3ca6"
	result, _ := client.histogramHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("expected %s got %s", expected, result)
	}

	expected = "c70681051e3af3de12f37686b6a4224f"
	path = "/irondb/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	result, _ = client.histogramHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("expected %s got %s", expected, result)
	}

}

func TestHistogramHandlerParseTimeRangeQuery(t *testing.T) {

	path := "/histogram/0/900/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	r, err := http.NewRequest(http.MethodGet, "http://0"+path, nil)
	if err != nil {
		t.Error(err)
	}

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()

	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)

	//tr := model.NewRequest("HistogramHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	// case where everything is good
	_, err = client.histogramHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}

	// case where the path is not long enough
	r.URL.Path = "/histogram/0/900/"
	expected := errors.ErrNotTimeRangeQuery
	_, err = client.histogramHandlerParseTimeRangeQuery(r)
	if err == nil || err != expected {
		t.Errorf("expected %s got %s", expected.Error(), err.Error())
	}

	// case where the start can't be parsed
	r.URL.Path = "/histogram/z/900/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	expected2 := `unable to parse timestamp z: strconv.ParseInt: parsing "z": invalid syntax`
	_, err = client.histogramHandlerParseTimeRangeQuery(r)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected %s got %s", expected2, err.Error())
	}

	// case where the end can't be parsed
	r.URL.Path = "/histogram/0/z/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, err = client.histogramHandlerParseTimeRangeQuery(r)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected %s got %s", expected2, err.Error())
	}

	// case where the period can't be parsed
	r.URL.Path = "/histogram/0/900/z/00112233-4455-6677-8899-aabbccddeeff/metric"
	expected2 = `unable to parse duration zs: time: invalid duration "zs"`
	_, err = client.histogramHandlerParseTimeRangeQuery(r)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected %s got %s", expected2, err.Error())
	}

}

func TestHistogramHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
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

	r = request.SetResources(r,
		request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error")))

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	client.histogramHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})
	if r.URL.Path != "/" {
		t.Errorf("expected %s got %s", "/", r.URL.Path)
	}

	// although SetExtent does not return a value to test, these lines exercise all coverage areas
	r.URL.Path = "/histogram/900/900/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	client.histogramHandlerSetExtent(r, nil, &timeseries.Extent{Start: now, End: now})

	r.URL.Path = "/histogram/900/900/300"
	trq := &timeseries.TimeRangeQuery{Step: 300 * time.Second}
	client.histogramHandlerSetExtent(r, trq, &timeseries.Extent{Start: then, End: now})

}

func TestHistogramHandlerFastForwardRequestError(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)
	r, err := http.NewRequest(http.MethodGet,
		"http://0/", nil)
	if err != nil {
		t.Error(err)
	}

	rsc := request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error"))
	r = request.SetResources(r, rsc)

	r.URL.Path = "/histogram/x/900/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, err = client.histogramHandlerFastForwardRequest(r)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}

	r.URL.Path = "/a/900/900/300/00112233-4455-6677-8899-aabbccddeeff/metric"
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: 300 * time.Second}
	_, err = client.histogramHandlerFastForwardRequest(r)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}

}
