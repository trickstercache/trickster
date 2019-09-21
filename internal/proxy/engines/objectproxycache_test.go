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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	tc "github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestObjectProxyCacheRequest(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	es := tu.NewTestServerHeaders(http.StatusOK, "test", headers)
	defer es.Close()

	p := &config.PathConfig{
		Path:            "/",
		CacheKeyParams:  []string{"query", "step", "time"},
		CacheKeyHeaders: []string{},
	}

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	log.Init()
	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"], cache: cache}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	r = r.WithContext(tc.WithConfigs(r.Context(), client.Configuration(), nil, p))

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, cache, time.Duration(60)*time.Second, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "test")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// get cache hit coverage too by repeating:

	w = httptest.NewRecorder()
	ObjectProxyCacheRequest(req, w, client, cache, time.Duration(60)*time.Second, false) // client Client, cache cache.Cache, ttl int, refresh bool, noLock bool) {
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "test")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "hit"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheRequestClientNoCache(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	es := tu.NewTestServerHeaders(http.StatusOK, "test", headers)
	defer es.Close()

	p := &config.PathConfig{
		Path:            "/",
		CacheKeyParams:  []string{"query", "step", "time"},
		CacheKeyHeaders: []string{},
	}

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	log.Init()
	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"], cache: cache}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	r = r.WithContext(tc.WithConfigs(r.Context(), client.Configuration(), nil, p))

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"Cache-Control": []string{"no-cache"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, cache, time.Duration(60)*time.Second, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "test")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "proxy-only"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheRequestOriginNoCache(t *testing.T) {

	headers := map[string]string{"Cache-Control": "no-cache"}
	es := tu.NewTestServerHeaders(http.StatusOK, "test", headers)
	defer es.Close()

	p := &config.PathConfig{
		Path:            "/",
		CacheKeyParams:  []string{"query", "step", "time"},
		CacheKeyHeaders: []string{},
	}

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	log.Init()
	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := &PromTestClient{config: config.Origins["default"], cache: cache}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	r = r.WithContext(tc.WithConfigs(r.Context(), client.Configuration(), nil, p))

	// get URL
	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, cache, time.Duration(60)*time.Second, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "test")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}
