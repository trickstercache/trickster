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
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

var testLogger = tl.ConsoleLogger("error")

func TestDoProxy(t *testing.T) {

	es := tu.NewTestServer(http.StatusOK, "test", nil)
	defer es.Close()

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", es.URL, "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:                  "/",
		RequestHeaders:        map[string]string{},
		ResponseHeaders:       map[string]string{},
		ResponseBody:          "test",
		ResponseBodyBytes:     []byte("test"),
		HasCustomResponseBody: true,
	}

	o.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer(), testLogger)))

	DoProxy(w, r, true)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url",
		badUpstream, "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	o.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", badUpstream, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer(), testLogger)))

	DoProxy(w, r, true)
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

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url",
		s.URL, "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	o.HTTPClient = http.DefaultClient
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", s.URL, nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer(), testLogger)))

	if testLogger.HasWarnedOnce("clockoffset.default") {
		t.Errorf("expected %t got %t", false, true)
	}

	DoProxy(w, r, true)
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

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", es.URL, "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
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

	o.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer(), testLogger)))

	// get URL
	DoProxy(w, r, true)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url",
		es.URL, "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
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

	o.HTTPClient = http.DefaultClient
	br := bytes.NewBuffer([]byte("test"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer(), testLogger)))

	// get URL
	DoProxy(w, r, true)
	resp := w.Result()

	err = testStatusCodeMatch(resp.StatusCode, http.StatusOK)
	if err != nil {
		t.Error(err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "http://example.com/", "-provider", "test", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	o.HTTPClient = http.DefaultClient

	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, nil, nil, nil, nil, tu.NewTestTracer(), testLogger)))
	r.Method = "\t"
	_, _, i := PrepareFetchReader(r)
	if i != 0 {
		t.Errorf("expected 0 got %d", i)
	}
}
