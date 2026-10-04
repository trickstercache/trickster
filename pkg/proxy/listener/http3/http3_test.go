/*
 * Copyright 2026 The Trickster Authors
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

package http3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	qh3 "github.com/quic-go/quic-go/http3"
)

func TestALPNAndQUICConfig(t *testing.T) {
	if ALPN != qh3.NextProtoH3 {
		t.Errorf("expected the h3 ALPN, got %q", ALPN)
	}
	qc := QUICConfig()
	if !qc.EnableDatagrams {
		t.Error("RFC 9221 datagrams should be negotiable")
	}
	if qc.Allow0RTT {
		t.Error("0-RTT should be off; it permits replay")
	}
}

func TestNewHandlerLimits(t *testing.T) {
	const idle, maxHeader = 90 * time.Second, 16384
	h := NewHandler(http.NotFoundHandler(),
		listener.ServerLimits{IdleTimeout: idle, MaxHeaderBytes: maxHeader})
	if svr := h.Server(); svr.IdleTimeout != idle || svr.MaxHeaderBytes != maxHeader {
		t.Errorf("limits not applied: idle %v, max header bytes %d", svr.IdleTimeout, svr.MaxHeaderBytes)
	}
}

func TestHandlerServesAndShutsDown(t *testing.T) {
	client, addr, stop := testEndpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	defer stop()
	resp, err := client.Get(addr)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "HTTP/3.0" {
		t.Errorf("expected an HTTP/3 request, got %q", body)
	}
}

func TestHandlerRefusesAfterShutdown(t *testing.T) {
	h := NewHandler(http.NotFoundHandler(), listener.ServerLimits{})
	// a server that never served shuts down at once
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, addr, stop := testEndpointWith(t, h)
	defer stop()
	if resp, err := client.Get(addr); err == nil {
		_ = resp.Body.Close()
		t.Error("a handler that has shut down should refuse new connections")
	}
}

func TestBodyDeadline(t *testing.T) {
	const header, read = time.Second, 5 * time.Second
	if got := bodyDeadline(listener.ServerLimits{ReadHeaderTimeout: header}); got != header {
		t.Errorf("without read_timeout got %v, want %v", got, header)
	}
	if got := bodyDeadline(listener.ServerLimits{ReadHeaderTimeout: header, ReadTimeout: read}); got != read {
		t.Errorf("with read_timeout got %v, want %v", got, read)
	}
}

func TestAltSvcAdvertiser(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := AltSvcAdvertiser(next, 8443)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil))
	got := rec.Header().Get(headers.NameAltSvc)
	if !strings.Contains(got, `h3=":8443"`) || !strings.Contains(got, "ma=2592000") {
		t.Errorf("unexpected Alt-Svc value: %q", got)
	}

	// an upgrade hijacks before headers are written; advertising is pointless
	up := httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
	up.Header.Set(headers.NameConnection, "Upgrade")
	up.Header.Set(headers.NameUpgrade, "websocket")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Header().Get(headers.NameAltSvc) != "" {
		t.Error("upgrade requests should not be advertised an alternative service")
	}

	// no advertised port means no wrapper at all
	rec = httptest.NewRecorder()
	AltSvcAdvertiser(next, 0).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil))
	if rec.Header().Get(headers.NameAltSvc) != "" {
		t.Error("expected no Alt-Svc when no port is advertised")
	}
}

func TestRequestDeadline(t *testing.T) {
	var served bool
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true })

	// with no timeout the handler is returned unwrapped
	if got := requestDeadline(next, 0); got == nil {
		t.Fatal("expected a handler")
	}
	h := requestDeadline(next, time.Second)
	r := httptest.NewRequest(http.MethodPost, "http://"+appinfo.Domain+"/",
		strings.NewReader("body"))
	// httptest.ResponseRecorder cannot set a read deadline; the handler must
	// still serve the request rather than failing on the attempt
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !served {
		t.Error("request was not served when the deadline could not be set")
	}
}
