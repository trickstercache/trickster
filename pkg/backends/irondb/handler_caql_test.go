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
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestCAQLHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "irondb", "/extension/lua/caql_v1"+
			"?query=metric:average(%2200112233-4455-6677-8899-aabbccddeeff%22,"+
			"%22metric%22)&start=0&end=900&period=300", "debug")
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

	client.CAQLHandler(w, r)
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

func TestCaqlHandlerSetExtent(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	// provide bad URL with no TimeRange query params
	cfg := bo.New()
	cfg.HTTPClient = tu.NewTestWebClient()
	cfg.Paths = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/extension/lua/caql_v1", nil)
	if err != nil {
		t.Error(err)
	}

	r = request.SetResources(r, request.NewResources(cfg, nil, nil, nil, client, nil, tl.ConsoleLogger("error")))

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.caqlHandlerSetExtent(nil, nil, nil)
	client.caqlHandlerSetExtent(r, nil, &timeseries.Extent{})
	client.caqlHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=7890"
	client.caqlHandlerSetExtent(r, nil, &timeseries.Extent{Start: now, End: now})

}

func TestCaqlHandlerParseTimeRangeQuery(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	cfg := bo.New()
	cfg.HTTPClient = tu.NewTestWebClient()
	cfg.Paths = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/extension/lua/caql_v1", nil)
	if err != nil {
		t.Error(err)
	}

	// case where everything is good
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=7890"
	trq, err := client.caqlHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Errorf("expected value got nil for %s", r.URL.RawQuery)
	}

	// missing q param but query is present
	r.URL.RawQuery = "help=1234&query=5678&start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
		return
	}

	// missing query param but q is present
	r.URL.RawQuery = "q=1234&start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}

	// missing query and q params
	r.URL.RawQuery = "start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing start param
	r.URL.RawQuery = "q=1234&query=5678&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// can't parse start param
	r.URL.RawQuery = "q=1234&query=5678&start=abcd&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing end param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// can't parse end param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=efgh&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing period param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// unparsable period param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=pqrs"
	_, err = client.caqlHandlerParseTimeRangeQuery(r)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

}

func TestCaqlHandlerFastForwardRequestError(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	_, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}",
		nil, "irondb", "/extension/lua/caql_v1", "debug")
	if err != nil {
		t.Error(err)
	}
	_, err = client.caqlHandlerFastForwardRequest(r)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}
}
