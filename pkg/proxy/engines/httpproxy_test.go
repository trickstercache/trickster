/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"bytes"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/config"
	tc "github.com/tricksterproxy/trickster/pkg/proxy/context"
	"github.com/tricksterproxy/trickster/pkg/proxy/forwarding"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	tu "github.com/tricksterproxy/trickster/pkg/util/testing"
)

var testLogger = tl.ConsoleLogger("error")

func TestDoProxy(t *testing.T) {

	es := tu.NewTestServer(http.StatusOK, "test", nil)
	defer es.Close()

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	pc := &po.Options{
		Path:                  "/",
		RequestHeaders:        map[string]string{},
		ResponseHeaders:       map[string]string{},
		ResponseBody:          "test",
		ResponseBodyBytes:     []byte("test"),
		HasCustomResponseBody: true,
	}

	oc.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, pc, nil, nil, nil, testLogger)))

	//req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())
	DoProxy(w, r)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestProxyRequestBadGateway(t *testing.T) {

	const badUpstream = "http://127.0.0.1:64389"

	// assume nothing listens on badUpstream, so this should force the proxy to generate a 502 Bad Gateway
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", badUpstream, "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	oc.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", badUpstream, br)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, pc, nil, nil, nil, testLogger)))

	//req := model.NewRequest("TestProxyRequest", r.Method, r.URL, make(http.Header), time.Duration(30)*time.Second, r, tu.NewTestWebClient())
	DoProxy(w, r)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusBadGateway)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}

}

func TestClockOffsetWarning(t *testing.T) {

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add(headers.NameDate, time.Now().Add(-1*time.Hour).Format(http.TimeFormat))
		w.WriteHeader(200)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", s.URL, "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	oc.HTTPClient = http.DefaultClient
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", s.URL, nil)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, pc, nil, nil, nil, testLogger)))

	if testLogger.HasWarnedOnce("clockoffset.default") {
		t.Errorf("expected %t got %t", false, true)
	}

	//req := model.NewRequest("TestProxyRequest", http.MethodGet, r.URL, make(http.Header), time.Duration(30)*time.Second, r, tu.NewTestWebClient())
	DoProxy(w, r)
	resp := w.Result()

	if !testLogger.HasWarnedOnce("clockoffset.default") {
		t.Errorf("expected %t got %t", true, false)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}

}

func TestDoProxyWithPCF(t *testing.T) {

	es := tu.NewTestServer(http.StatusOK, "test", nil)
	defer es.Close()

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	pc := &po.Options{
		Path:                    "/",
		RequestHeaders:          map[string]string{},
		ResponseHeaders:         map[string]string{},
		ResponseBody:            "test",
		ResponseBodyBytes:       []byte("test"),
		HasCustomResponseBody:   true,
		CollapsedForwardingName: "progressive",
		CollapsedForwardingType: forwarding.CFTypeProgressive,
	}

	oc.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, pc, nil, nil, nil, testLogger)))

	// get URL

	//req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())
	DoProxy(w, r)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestProxyRequestWithPCFMultipleClients(t *testing.T) {

	es := tu.NewTestServer(http.StatusOK, "test", nil)
	defer es.Close()

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	pc := &po.Options{
		Path:                    "/",
		RequestHeaders:          map[string]string{},
		ResponseHeaders:         map[string]string{},
		ResponseBody:            "test",
		ResponseBodyBytes:       []byte("test"),
		HasCustomResponseBody:   true,
		CollapsedForwardingName: "progressive",
		CollapsedForwardingType: forwarding.CFTypeProgressive,
	}

	oc.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, pc, nil, nil, nil, testLogger)))

	// get URL

	//req := model.NewRequest("TestProxyRequest", r.Method, r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r, tu.NewTestWebClient())
	DoProxy(w, r)
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

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestPrepareFetchReaderErr(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://example.com/", "-origin-type", "test", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	oc.HTTPClient = http.DefaultClient

	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(oc, nil, nil, nil, nil, testLogger)))
	r.Method = "\t"
	_, _, i := PrepareFetchReader(r)
	if i != 0 {
		t.Errorf("expected 0 got %d", i)
	}
}
