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
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	qh3 "github.com/quic-go/quic-go/http3"
)

func TestNewServerForcesH3ALPN(t *testing.T) {
	base := &tls.Config{NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
	svr := NewServer(http.NotFoundHandler(), base, 8443, 0)
	if len(svr.TLSConfig.NextProtos) != 1 || svr.TLSConfig.NextProtos[0] != qh3.NextProtoH3 {
		t.Errorf("expected h3 to be the sole ALPN, got %v", svr.TLSConfig.NextProtos)
	}
	// the caller's config must not be mutated: the TCP listener still needs h2
	if len(base.NextProtos) != 2 {
		t.Errorf("source TLS config was mutated: %v", base.NextProtos)
	}
	if svr.Port != 8443 {
		t.Errorf("expected advertised port 8443, got %d", svr.Port)
	}
	if !svr.QUICConfig.EnableDatagrams {
		t.Error("RFC 9221 datagrams should be negotiable")
	}
	if svr.QUICConfig.Allow0RTT {
		t.Error("0-RTT should be off by default; it permits replay")
	}
}

func TestAltSvcAdvertiser(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := AltSvcAdvertiser(next, 8443)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil))
	got := rec.Header().Get(headers.NameAltSvc)
	if !strings.Contains(got, `h3=":8443"`) || !strings.Contains(got, "ma=2592000") {
		t.Errorf("unexpected Alt-Svc value: %q", got)
	}

	// an upgrade hijacks before headers are written; advertising is pointless
	up := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
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
		httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil))
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
	r := httptest.NewRequest(http.MethodPost, "http://trickstercache.org/",
		strings.NewReader("body"))
	// httptest.ResponseRecorder cannot set a read deadline; the handler must
	// still serve the request rather than failing on the attempt
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !served {
		t.Error("request was not served when the deadline could not be set")
	}
}
