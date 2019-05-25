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
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	tu "github.com/Comcast/trickster/internal/util/testing"
	"github.com/Comcast/trickster/pkg/promsim"
)

func TestDeltaProxyCacheRequest(t *testing.T) {

	es := promsim.NewTestServer()
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"]}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	query := "some_query_here{}"
	step := time.Duration(300) * time.Second
	start := time.Now().Add(-time.Duration(6) * time.Hour).Truncate(step)
	fmt.Println("*", start)
	end := time.Now().Truncate(step)
	fmt.Println("*", end)

	expected, _ := promsim.GetTimeSeriesData(query, start, end, step)

	u := r.URL
	u.Path = "/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()), start.Unix(), end.Unix(), query)

	req := model.NewRequest("default", "test", "TestDeltaProxyCacheRequest", "GET", u, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r)

	DeltaProxyCacheRequest(req, w, client, cache, 60, false)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected '%s' got '%s'.", expected, bodyBytes)
	}

	// get cache hit coverage too by repeating:

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", es.URL, nil)
	DeltaProxyCacheRequest(req, w, client, cache, 60, false) // client Client, cache cache.Cache, ttl int, refresh bool, noLock bool) {
	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected '%s' got '%s'.", expected, bodyBytes)
	}

}

func TestDeltaProxyCacheRequestBadGateway(t *testing.T) {

	es := tu.NewTestServer(502, "")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"]}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	// get URL

	req := model.NewRequest("default", "test", "TestDeltaProxyCacheRequestBadGateway", "GET", r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r)

	DeltaProxyCacheRequest(req, w, client, cache, 60, false)

	resp := w.Result()

	// it should return 502 Bad Gateway
	if resp.StatusCode != 502 {
		t.Errorf("expected 502 got %d.", resp.StatusCode)
	}

}

func TestDeltaProxyCacheRequestParseError(t *testing.T) {

	es := tu.NewTestServer(502, "")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"]}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	// get URL

	req := model.NewRequest("default", "test", "TestProxyRequestParseError", "GET", r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r)

	DeltaProxyCacheRequest(req, w, client, cache, 60, false)

}
