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
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestTextHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		"{}", nil, "irondb", "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric", "debug")
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

	client.TextHandler(w, r)
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

func TestTextHandlerDeriveCacheKey(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	path := "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", path, "debug")
	if err != nil {
		t.Error(err)
	}

	const expected = "a506d1700414b1d0ac15340bd619fdab"
	result, _ := client.textHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("expected %s got %s", expected, result)
	}

}

func TestTextHandlerParseTimeRangeQuery(t *testing.T) {

	path := "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
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

	//tr := model.NewRequest("RollupHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	// case where everything is good
	_, err = client.textHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}

	// case where the path is not long enough
	r.URL.Path = "/read/0/900/"
	expected := errors.ErrNotTimeRangeQuery
	_, err = client.textHandlerParseTimeRangeQuery(r)
	if err == nil || err != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	// case where the start can't be parsed
	r.URL.Path = "/read/z/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	expected2 := `unable to parse timestamp z: strconv.ParseInt: parsing "z": invalid syntax`
	_, err = client.textHandlerParseTimeRangeQuery(r)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected %s got %s", expected2, err.Error())
	}

	// case where the end can't be parsed
	r.URL.Path = "/read/0/z/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, err = client.textHandlerParseTimeRangeQuery(r)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected %s got %s", expected2, err.Error())
	}

}

func TestTextHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)
	r, err := http.NewRequest(http.MethodGet, "http://0/test", nil)
	if err != nil {
		t.Error(err)
	}
	//tr := model.NewRequest("TextHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	client.textHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})
	if r.URL.Path != "/test" {
		t.Errorf("expected %s got %s", "/test", r.URL.Path)
	}

}
