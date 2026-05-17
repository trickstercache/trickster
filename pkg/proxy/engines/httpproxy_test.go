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

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

var testLogger = logging.ConsoleLogger("warn")

const testResponseBody = "test"

func TestDoProxy(t *testing.T) {
	logger.SetLogger(testLogger)
	es := tu.NewTestServer(http.StatusOK, testResponseBody, nil)
	defer es.Close()

	conf, err := config.Load([]string{"-origin-url", es.URL, "-provider", testResponseBody, "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:              "/",
		RequestHeaders:    map[string]string{},
		ResponseHeaders:   map[string]string{},
		ResponseBody:      new(testResponseBody),
		ResponseBodyBytes: []byte(testResponseBody),
	}

	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	br := bytes.NewBuffer([]byte(testResponseBody))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))

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

	err = testStringMatch(string(bodyBytes), testResponseBody)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestProxyRequestBadGateway(t *testing.T) {
	logger.SetLogger(testLogger)
	const badUpstream = "http://127.0.0.1:64389"

	// assume nothing listens on badUpstream, so this should force the proxy to generate a 502 Bad Gateway
	conf, err := config.Load([]string{
		"-origin-url",
		badUpstream, "-provider", testResponseBody, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	br := bytes.NewBuffer([]byte(testResponseBody))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", badUpstream, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))

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
		w.WriteHeader(http.StatusOK)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	defer s.Close()

	conf, err := config.Load([]string{
		"-origin-url",
		s.URL, "-provider", testResponseBody, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	o := conf.Backends["default"]
	pc := &po.Options{
		Path:            "/",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}

	o.Name = "default"
	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", s.URL, nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))
	logger.SetLogger(testLogger)
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
	logger.SetLogger(testLogger)
	es := tu.NewTestServer(http.StatusOK, testResponseBody, nil)
	defer es.Close()

	conf, err := config.Load([]string{"-origin-url", es.URL, "-provider", testResponseBody, "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:                    po.DefaultPath,
		RequestHeaders:          map[string]string{},
		ResponseHeaders:         map[string]string{},
		ResponseBody:            new(testResponseBody),
		ResponseBodyBytes:       []byte(testResponseBody),
		CollapsedForwardingName: forwarding.CFNameProgressive,
		CollapsedForwardingType: forwarding.CFTypeProgressive,
	}

	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	br := bytes.NewBuffer([]byte(testResponseBody))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))

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

	err = testStringMatch(string(bodyBytes), testResponseBody)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestProxyRequestWithPCFMultipleClients(t *testing.T) {
	logger.SetLogger(testLogger)
	es := tu.NewTestServer(http.StatusOK, testResponseBody, nil)
	defer es.Close()

	conf, err := config.Load([]string{
		"-origin-url",
		es.URL, "-provider", testResponseBody, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	pc := &po.Options{
		Path:                    "/",
		RequestHeaders:          map[string]string{},
		ResponseHeaders:         map[string]string{},
		ResponseBody:            new(testResponseBody),
		ResponseBodyBytes:       []byte(testResponseBody),
		CollapsedForwardingName: forwarding.CFNameProgressive,
		CollapsedForwardingType: forwarding.CFTypeProgressive,
	}

	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	br := bytes.NewBuffer([]byte(testResponseBody))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, br)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))

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

	err = testStringMatch(string(bodyBytes), testResponseBody)
	if err != nil {
		t.Error(err)
	}

	err = testResultHeaderPartMatch(resp.Header, map[string]string{"engine": "HTTPProxy"})
	if err != nil {
		t.Error(err)
	}
}

func TestRespond(t *testing.T) {
	w := httptest.NewRecorder()
	h := http.Header{"X-Custom": {"val"}}
	body := bytes.NewReader([]byte("response body"))

	Respond(w, http.StatusCreated, h, body)

	resp := w.Result()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, resp.StatusCode)
	}
	if resp.Header.Get("X-Custom") != "val" {
		t.Errorf("expected X-Custom header to be set")
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "response body" {
		t.Errorf("expected body %q, got %q", "response body", string(b))
	}
}

func TestRespondNilBody(t *testing.T) {
	w := httptest.NewRecorder()
	h := http.Header{"X-Test": {"1"}}

	Respond(w, http.StatusNoContent, h, nil)

	resp := w.Result()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if len(b) != 0 {
		t.Errorf("expected empty body, got %q", string(b))
	}
}

func TestRespondPlainWriter(t *testing.T) {
	var buf bytes.Buffer
	h := http.Header{"X-Ignored": {"yes"}}
	body := bytes.NewReader([]byte("plain writer body"))

	Respond(&buf, http.StatusOK, h, body)

	// plain writer doesn't handle headers/status, but body should be written
	if buf.String() != "plain writer body" {
		t.Errorf("expected body %q, got %q", "plain writer body", buf.String())
	}
}

func TestPrepareResponseWriterMergesHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	upstream := http.Header{
		"X-Upstream": {"upstream-val"},
	}

	result := PrepareResponseWriter(w, http.StatusOK, upstream)
	if result == nil {
		t.Fatal("expected non-nil writer")
	}

	// upstream headers should be merged into the response
	if w.Header().Get("X-Upstream") != "upstream-val" {
		t.Errorf("expected upstream header to be merged")
	}
}

func TestPrepareResponseWriterPlainWriter(t *testing.T) {
	var buf bytes.Buffer
	upstream := http.Header{"X-Test": {"val"}}

	result := PrepareResponseWriter(&buf, http.StatusOK, upstream)
	// should return the same writer as-is
	if result != &buf {
		t.Errorf("expected plain writer to be returned unchanged")
	}
}

// TestPrepareResponseWriterStripsHopByHop pins the response-side hop-by-hop
// strip. An upstream that emits `Connection: X-Internal-Auth` plus
// `X-Internal-Auth: <secret>` must not leak X-Internal-Auth to the client,
// per RFC 7230 6.1. The static HopHeaders set (Connection, Keep-Alive,
// Proxy-Authenticate, Proxy-Authorization, Te, Trailer, Transfer-Encoding,
// Upgrade) must also be stripped.
func TestPrepareResponseWriterStripsHopByHop(t *testing.T) {
	tests := []struct {
		name     string
		upstream http.Header
		mustGo   []string // headers that must NOT appear downstream
		mustKeep []string // headers that MUST appear downstream
	}{
		{
			name: "named in Connection: custom token stripped",
			upstream: http.Header{
				"Connection":      {"X-Internal-Auth"},
				"X-Internal-Auth": {"leaked-token"},
				"X-Safe":          {"keep"},
			},
			mustGo:   []string{"X-Internal-Auth", "Connection"},
			mustKeep: []string{"X-Safe"},
		},
		{
			name: "empty token then Authorization (CVE-2021-33197 shape)",
			upstream: http.Header{
				"Connection":    {", Authorization"},
				"Authorization": {"Bearer leaked"},
				"Content-Type":  {"text/plain"},
			},
			mustGo:   []string{"Authorization", "Connection"},
			mustKeep: []string{"Content-Type"},
		},
		{
			name: "static hop-by-hop list always stripped",
			upstream: http.Header{
				"Keep-Alive":          {"timeout=5"},
				"Proxy-Authenticate":  {"Basic realm=upstream"},
				"Proxy-Authorization": {"Basic abc"},
				"Te":                  {"trailers"},
				"Trailer":             {"Expires"},
				"Transfer-Encoding":   {"chunked"},
				"Upgrade":             {"websocket"},
				"Content-Type":        {"application/json"},
			},
			mustGo: []string{
				"Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
				"Te", "Trailer", "Transfer-Encoding", "Upgrade",
			},
			mustKeep: []string{"Content-Type"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			PrepareResponseWriter(w, http.StatusOK, tc.upstream)
			got := w.Header()
			for _, h := range tc.mustGo {
				if vals := got.Values(h); len(vals) > 0 {
					t.Errorf("header %q must not be forwarded to client, got %v", h, vals)
				}
			}
			for _, h := range tc.mustKeep {
				if got.Get(h) == "" {
					t.Errorf("header %q must be forwarded to client, missing", h)
				}
			}
		})
	}
}

func TestSetStatusHeader(t *testing.T) {
	tests := []struct {
		httpStatus     int
		expectedStatus string
	}{
		{http.StatusOK, "proxy-only"},
		{http.StatusNoContent, "proxy-only"},
		{http.StatusBadRequest, "proxy-error"},
		{http.StatusInternalServerError, "proxy-error"},
		{http.StatusBadGateway, "proxy-error"},
	}

	for _, tt := range tests {
		h := http.Header{}
		st := setStatusHeader(tt.httpStatus, h)
		if st.String() != tt.expectedStatus {
			t.Errorf("httpStatus=%d: expected %q, got %q",
				tt.httpStatus, tt.expectedStatus, st.String())
		}
		// verify header was set
		result := h.Get(headers.NameTricksterResult)
		if result == "" {
			t.Errorf("httpStatus=%d: expected Trickster-Result header to be set", tt.httpStatus)
		}
	}
}

func TestPrepareFetchReaderErr(t *testing.T) {
	logger.SetLogger(testLogger)
	conf, err := config.Load([]string{
		"-origin-url", "http://example.com/",
		"-provider", testResponseBody, "-log-level", "debug",
	})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, nil, nil, nil, nil, tu.NewTestTracer())))
	r.Method = "\t"
	_, _, i := PrepareFetchReader(r)
	if i != 0 {
		t.Errorf("expected 0 got %d", i)
	}
}
