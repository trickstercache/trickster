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

package handler

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func mustList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("next"))
	})
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	allow := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.RemoteAddr = "192.0.2.9:1"
	w := httptest.NewRecorder()
	Middleware(nil, "office", ScopeListener, next).ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "next" {
		t.Fatalf("nil list = %d %q", w.Code, w.Body.String())
	}
	if Middleware(deny, "office", ScopeListener, nil) != nil {
		t.Fatal("nil next must pass through")
	}

	t.Run("deny", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(deny, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests || w.Body.Len() != 0 {
			t.Fatalf("deny = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("allow", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(allow, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "next" {
			t.Fatalf("allow = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("readiness is judged", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/trickster/ready", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(deny, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests || w.Body.String() == "next" {
			t.Fatalf("ready = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("invalid address", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "not-an-address"
		w := httptest.NewRecorder()
		Middleware(allow, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid = %d", w.Code)
		}
	})

	t.Run("client ip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "10.1.1.1:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "192.0.2.9"))
		w := httptest.NewRecorder()
		Middleware(allow, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("client ip = %d", w.Code)
		}
	})

	t.Run("http peer is not judged again", func(t *testing.T) {
		peer := mustList(t, ipacl.Options{Allow: []string{"10.1.1.1"}, Source: "peer"})
		req := httptest.NewRequest(http.MethodGet, "/trickster/ready", nil)
		req.RemoteAddr = "198.51.100.8:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "10.1.1.1"))
		w := httptest.NewRecorder()
		Middleware(peer, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "next" {
			t.Fatalf("http/1 peer = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("http3 peer", func(t *testing.T) {
		peer := mustList(t, ipacl.Options{Allow: []string{"10.1.1.1"}, Source: "peer"})
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.ProtoMajor = 3
		req.RemoteAddr = "10.1.1.1:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "192.0.2.9"))
		w := httptest.NewRecorder()
		Middleware(peer, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("http/3 peer allow = %d", w.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/trickster/ready", nil)
		req.ProtoMajor = 3
		req.RemoteAddr = "198.51.100.8:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "10.1.1.1"))
		w = httptest.NewRecorder()
		Middleware(peer, "office", ScopeListener, next).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || w.Body.String() == "next" {
			t.Fatalf("http/3 peer deny = %d %q", w.Code, w.Body.String())
		}
	})
}

func TestMiddlewareCountsEachScope(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	allow := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}})
	for _, scope := range []string{ScopeListener, ScopeBackend, ScopePath} {
		name := "http-" + scope
		beforeAllow := ipaclDecisions(name, scope, "allow")
		beforeDeny := ipaclDecisions(name, scope, "deny")

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.9:1"
		Middleware(allow, name, scope, next).ServeHTTP(httptest.NewRecorder(), req)
		if got := ipaclDecisions(name, scope, "allow"); got != beforeAllow+1 {
			t.Fatalf("%s allow = %v, want %v", scope, got, beforeAllow+1)
		}

		req = httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "198.51.100.1:1"
		w := httptest.NewRecorder()
		Middleware(deny, name, scope, next).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s deny status = %d", scope, w.Code)
		}
		if got := ipaclDecisions(name, scope, "deny"); got != beforeDeny+1 {
			t.Fatalf("%s deny = %v, want %v", scope, got, beforeDeny+1)
		}
	}
}

func ipaclDecisions(name, scope, verdict string) float64 {
	return testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(name, scope, verdict))
}

func TestMiddlewareRejectSetsNoStore(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	list := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	before := ipaclDecisions("reject-office", ScopeListener, "deny")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.9:1"
	w := httptest.NewRecorder()
	Middleware(list, "reject-office", ScopeListener, next).ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests || w.Body.Len() != 0 || called {
		t.Fatalf("reject = %d %q called=%v", w.Code, w.Body.String(), called)
	}
	if got := w.Header().Get(headers.NameCacheControl); got != headers.ValueNoStore {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := ipaclDecisions("reject-office", ScopeListener, "deny"); got != before+1 {
		t.Fatalf("deny = %v, want %v", got, before+1)
	}
}

func TestMiddlewareDropPanics(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	list := mustList(t, ipacl.Options{Action: "drop"})
	before := ipaclDecisions("drop-office", ScopeListener, "deny")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.9:1"
	w := &statusWriter{}
	defer func() {
		got := recover()
		if got != http.ErrAbortHandler {
			t.Fatalf("panic = %v", got)
		}
		if w.code != 0 || w.body.Len() != 0 || called {
			t.Fatalf("drop wrote %d %q called=%v", w.code, w.body.String(), called)
		}
		if after := ipaclDecisions("drop-office", ScopeListener, "deny"); after != before+1 {
			t.Fatalf("deny = %v, want %v", after, before+1)
		}
	}()
	Middleware(list, "drop-office", ScopeListener, next).ServeHTTP(w, req)
	t.Fatal("drop returned")
}

type statusWriter struct { // records a status only once WriteHeader runs, where a ResponseRecorder starts at 200
	h    http.Header
	code int
	body bytes.Buffer
}

func (w *statusWriter) Header() http.Header {
	if w.h == nil {
		w.h = make(http.Header)
	}
	return w.h
}

func (w *statusWriter) WriteHeader(code int) { w.code = code }

func (w *statusWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func TestDropWritesNoAccessLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	l, err := accesslog.NewLogger(&alo.Options{
		Filename: path, ErrorFilename: "", Format: "%>s",
	}, 0, accesslog.UnmatchedName, accesslog.UnmatchedName)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	drop := mustList(t, ipacl.Options{Action: "drop"})
	func() {
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("panic = %v", got)
			}
		}()
		accesslog.RouterMiddleware(l, Middleware(drop, "log-drop", ScopeListener, next)).
			ServeHTTP(httptest.NewRecorder(), requestFrom("192.0.2.9:1"))
	}()
	reject := mustList(t, ipacl.Options{Status: http.StatusForbidden})
	accesslog.RouterMiddleware(l, Middleware(reject, "log-reject", ScopeListener, next)).
		ServeHTTP(httptest.NewRecorder(), requestFrom("192.0.2.9:1"))
	l.Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "403") {
		t.Fatalf("access log = %q", body)
	}
}

func TestDropClosesHTTP11(t *testing.T) {
	drop := mustList(t, ipacl.Options{Action: "drop"})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("next"))
	})
	srv := httptest.NewServer(Middleware(drop, "http11-drop", ScopeListener, next))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if len(body) != 0 || (err != nil && !strings.Contains(err.Error(), "EOF") && err != io.EOF) {
		t.Fatalf("read %q %v", body, err)
	}
	if bytes.Contains(body, []byte("HTTP/")) {
		t.Fatalf("status line %q", body)
	}
}

func TestDropResetsHTTP2(t *testing.T) {
	drop := mustList(t, ipacl.Options{Action: "drop"})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(h2c.NewHandler(
		Middleware(drop, "http2-drop", ScopeListener, next), &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	resp, err := client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, ok := errors.AsType[http2.StreamError](err); !ok {
		t.Fatalf("err = %T %v", err, err)
	}
}

func TestMiddlewareDebugDenialLog(t *testing.T) {
	buf := debugLog(t)
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}})
	req := requestFrom("192.0.2.9:9")
	Middleware(deny, "office", ScopeListener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
	})).ServeHTTP(httptest.NewRecorder(), req)
	assertDenial(t, buf.String(), "office", ScopeListener, "192.0.2.9", "reject")

	buf.Reset()
	allow := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})
	Middleware(allow, "office", ScopeListener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), req)
	if buf.Len() != 0 {
		t.Fatalf("allow logged %q", buf.String())
	}
}

func TestMiddlewareDenialIsDebugOnly(t *testing.T) {
	buf := &bytes.Buffer{}
	l := logging.StreamLogger(buf, level.Info)
	l.SetLogAsynchronous(false)
	logger.SetLogger(l)
	t.Cleanup(func() { logger.SetLogger(logging.NoopLogger()) })
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}})
	Middleware(deny, "office", ScopeListener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
	})).ServeHTTP(httptest.NewRecorder(), requestFrom("192.0.2.9:9"))
	if buf.Len() != 0 {
		t.Fatalf("info logged %q", buf.String())
	}
}

func debugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	l := logging.StreamLogger(buf, level.Debug)
	l.SetLogAsynchronous(false)
	logger.SetLogger(l)
	t.Cleanup(func() { logger.SetLogger(logging.NoopLogger()) })
	return buf
}

func assertDenial(t *testing.T, line, name, scope, addr, action string) {
	t.Helper()
	for _, want := range []string{
		"level=debug",
		"ip_acl=" + name,
		"scope=" + scope,
		"address=" + addr,
		"action=" + action,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q missing %q", line, want)
		}
	}
}

func requestFrom(remote string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remote
	return req
}
