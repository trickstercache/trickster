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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func setupTestHarnessOPC(file, body string, code int, headers map[string]string) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *PromTestClient, error) {

	client := &PromTestClient{}
	ts, w, r, hc, err := tu.NewTestInstance(file, client.DefaultPathConfigs, code, body, headers, "prometheus", "/api/v1/query", "debug")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	pc := tc.PathConfig(r.Context())
	if pc == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find path %s", "/api/v1/query")
	}

	oc := tc.OriginConfig(r.Context())
	cc := tc.CacheClient(r.Context())

	client.cache = cc
	client.webClient = hc
	client.config = oc

	pc.CacheKeyParams = []string{"rangeKey", "instantKey"}

	return ts, w, r, client, nil
}

func setupTestHarnessOPCWithPCF(file, body string, code int, headers map[string]string) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *PromTestClient, error) {

	client := &PromTestClient{}
	ts, w, r, hc, err := tu.NewTestInstance(file, client.DefaultPathConfigs, code, body, headers, "prometheus", "/api/v1/query", "debug")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	pc := tc.PathConfig(r.Context())
	if pc == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find path %s", "/api/v1/query")
	}

	pc.CollapsedForwardingName = "progressive"
	pc.CollapsedForwardingType = config.CFTypeProgressive

	oc := tc.OriginConfig(r.Context())
	cc := tc.CacheClient(r.Context())

	client.cache = cc
	client.webClient = hc
	client.config = oc

	pc.CacheKeyParams = []string{"rangeKey", "instantKey"}

	return ts, w, r, client, nil
}

func TestObjectProxyCacheRequest(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	oc := tc.OriginConfig(r.Context())
	oc.MaxTTLSecs = 15
	oc.MaxTTL = time.Duration(oc.MaxTTLSecs) * time.Second

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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
	ObjectProxyCacheRequest(req, w, client, false)
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

func TestObjectProxyCacheRequestWithPCF(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPCWithPCF("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	oc := tc.OriginConfig(r.Context())
	oc.MaxTTLSecs = 15
	oc.MaxTTL = time.Duration(oc.MaxTTLSecs) * time.Second

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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
	ObjectProxyCacheRequest(req, w, client, false)
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
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"Cache-Control": []string{"no-cache"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL
	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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

func TestObjectProxyCacheIMS(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Modified-Since": []string{"Sun, 16 Jun 2019 14:19:04 GMT"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheIUS(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Unmodified-Since": []string{"Sun, 16 Jun 2019 14:19:04 GMT"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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

func TestObjectProxyCacheIM(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60", "ETag": "test"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Match": []string{"test2"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheINM(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60", "ETag": "test"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-None-Match": []string{"test"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheNoRevalidate(t *testing.T) {

	headers := map[string]string{headers.NameCacheControl: headers.ValueMaxAge + "=1"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = headers

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)
	w = httptest.NewRecorder()
	ObjectProxyCacheRequest(req, w, client, false)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheCanRevalidate(t *testing.T) {

	headers := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=1",
		headers.NameETag:         "test-etag",
		headers.NameLastModified: "Sun, 16 Jun 2019 14:19:04 GMT",
	}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = headers

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)
	w = httptest.NewRecorder()
	ObjectProxyCacheRequest(req, w, client, false)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheRevalidated(t *testing.T) {

	hdr := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=1",
		headers.NameLastModified: "Sun, 16 Jun 2019 14:19:04 GMT",
	}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, hdr)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = hdr

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)

	s := tu.NewTestServer(304, "", nil)
	r2 := httptest.NewRequest("GET", s.URL+"/api/v1/query", nil)
	p = tu.NewTestPathConfig(tc.OriginConfig(r.Context()), client.DefaultPathConfigs, "/api/v1/query")
	r2 = r2.WithContext(tc.WithConfigs(r2.Context(), tc.OriginConfig(r.Context()), tc.CacheClient(r.Context()), p))

	req = model.NewRequest("TestProxyRequest", r2.Method, r2.URL, http.Header{}, time.Duration(30)*time.Second, r2, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	h := w.Header()
	h.Set(headers.NameIfMatch, "test")

	ObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

}

func TestObjectProxyCacheRequestNegativeCache(t *testing.T) {

	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusNotFound, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	pc := config.NewPathConfig()
	cfg := client.Configuration()
	cfg.Paths = map[string]*config.PathConfig{
		"/": pc,
	}
	r = r.WithContext(tc.WithConfigs(r.Context(), cfg, client.Cache(), pc))

	// request the url, it should respond with a 404 cache miss
	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, nil, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	ObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	// request again, should still cache miss, but this time, put 404's into the Negative Cache for 30s
	cfg.NegativeCache[404] = time.Second * 30

	req = model.NewRequest("TestProxyRequest", r.Method, r.URL, nil, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	ObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// request again, this time it should be a cache hit.
	req = model.NewRequest("TestProxyRequest", r.Method, r.URL, nil, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	ObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "nchit"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheRequest(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	oc := tc.OriginConfig(r.Context())
	oc.MaxTTLSecs = 15
	oc.MaxTTL = time.Duration(oc.MaxTTLSecs) * time.Second

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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
	SequentialObjectProxyCacheRequest(req, w, client, false)
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

func TestSequentialObjectProxyCacheRequestClientNoCache(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"Cache-Control": []string{"no-cache"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

func TestSequentialObjectProxyCacheRequestOriginNoCache(t *testing.T) {

	headers := map[string]string{"Cache-Control": "no-cache"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL
	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

func TestSequentialObjectProxyCacheIMS(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Modified-Since": []string{"Sun, 16 Jun 2019 14:19:04 GMT"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheIUS(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Unmodified-Since": []string{"Sun, 16 Jun 2019 14:19:04 GMT"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

func TestSequentialObjectProxyCacheIM(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60", "ETag": "test"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-Match": []string{"test2"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheINM(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60", "ETag": "test"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"If-None-Match": []string{"test"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	err = testStringMatch(string(bodyBytes), "")
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheNoRevalidate(t *testing.T) {

	headers := map[string]string{headers.NameCacheControl: headers.ValueMaxAge + "=1"}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = headers

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)
	w = httptest.NewRecorder()
	SequentialObjectProxyCacheRequest(req, w, client, false)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheCanRevalidate(t *testing.T) {

	headers := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=1",
		headers.NameETag:         "test-etag",
		headers.NameLastModified: "Sun, 16 Jun 2019 14:19:04 GMT",
	}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = headers

	// get URL

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)
	w = httptest.NewRecorder()
	SequentialObjectProxyCacheRequest(req, w, client, false)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheRevalidated(t *testing.T) {

	hdr := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=1",
		headers.NameLastModified: "Sun, 16 Jun 2019 14:19:04 GMT",
	}
	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusOK, hdr)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := tc.PathConfig(r.Context())
	p.ResponseHeaders = hdr

	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
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

	time.Sleep(1010 * time.Millisecond)

	s := tu.NewTestServer(304, "", nil)
	r2 := httptest.NewRequest("GET", s.URL+"/api/v1/query", nil)
	p = tu.NewTestPathConfig(tc.OriginConfig(r.Context()), client.DefaultPathConfigs, "/api/v1/query")
	r2 = r2.WithContext(tc.WithConfigs(r2.Context(), tc.OriginConfig(r.Context()), tc.CacheClient(r.Context()), p))

	req = model.NewRequest("TestProxyRequest", r2.Method, r2.URL, http.Header{}, time.Duration(30)*time.Second, r2, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	h := w.Header()
	h.Set(headers.NameIfMatch, "test")

	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotModified)
	if err != nil {
		t.Error(err)
	}

}

func TestSequentialObjectProxyCacheRequestNegativeCache(t *testing.T) {

	ts, w, r, client, err := setupTestHarnessOPC("", "test", http.StatusNotFound, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	pc := config.NewPathConfig()
	cfg := client.Configuration()
	cfg.Paths = map[string]*config.PathConfig{
		"/": pc,
	}
	r = r.WithContext(tc.WithConfigs(r.Context(), cfg, client.Cache(), pc))

	// request the url, it should respond with a 404 cache miss
	req := model.NewRequest("TestProxyRequest", r.Method, r.URL, make(http.Header), time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	// request again, should still cache miss, but this time, put 404's into the Negative Cache for 30s
	cfg.NegativeCache[404] = time.Second * 30

	req = model.NewRequest("TestProxyRequest", r.Method, r.URL, make(http.Header), time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "kmiss"})
	if err != nil {
		t.Error(err)
	}

	// request again, this time it should be a cache hit.
	req = model.NewRequest("TestProxyRequest", r.Method, r.URL, nil, time.Duration(30)*time.Second, r, tu.NewTestWebClient())

	w = httptest.NewRecorder()
	SequentialObjectProxyCacheRequest(req, w, client, false)
	resp = w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusNotFound)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"status": "nchit"})
	if err != nil {
		t.Error(err)
	}

}
