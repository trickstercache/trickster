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

package accesslog

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	authtypes "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	utilmiddleware "github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

func testLoggerOptions(t *testing.T) *alo.Options {
	t.Helper()
	dir := t.TempDir()
	return &alo.Options{
		Filename:      filepath.Join(dir, "test.access.log"),
		ErrorFilename: filepath.Join(dir, "test.error.log"),
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s failed: %v", path, err)
	}
	return string(b)
}

func TestNewLoggerDisabled(t *testing.T) {
	l, err := NewLogger(nil, 0, "b", "p")
	if l != nil || err != nil {
		t.Errorf("expected nil logger for nil options, got %v, %v", l, err)
	}
	l, err = NewLogger(&alo.Options{}, 0, "b", "p")
	if l != nil || err != nil {
		t.Errorf("expected nil logger for empty options, got %v, %v", l, err)
	}
}

func TestNewLoggerBadFormat(t *testing.T) {
	o := testLoggerOptions(t)
	o.Format = "%Z"
	if _, err := NewLogger(o, 0, "b", "p"); err == nil {
		t.Error("expected format error")
	}
	o.Format = ""
	o.ErrorFormat = "%Z"
	if _, err := NewLogger(o, 0, "b", "p"); err == nil {
		t.Error("expected error format error")
	}
}

func TestNewLoggerInstanceID(t *testing.T) {
	o := testLoggerOptions(t)
	o.ErrorFilename = ""
	l, err := NewLogger(o, 2, "b", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasSuffix(l.access.Filename(), "test.access.2.log") {
		t.Errorf("expected instance filename, got %s", l.access.Filename())
	}
}

func TestMiddlewareAccessAndErrorLogs(t *testing.T) {
	o := testLoggerOptions(t)
	o.Format = `%h %u "%r" %>s %b %{cache-status}x %{backend}x %{provider}x %{path-config}x`
	l, err := NewLogger(o, 0, "example1", "rp")
	if err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fail" {
			request.GetResources(r).AuthResult = &authtypes.AuthResult{
				Status: authtypes.AuthSuccess, Username: "frank"}
		}
		w.Header().Set(headers.NameTricksterResult, "engine=HTTPProxy; status=hit")
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("upstream error"))
			return
		}
		w.Write([]byte("hello"))
	})
	h := Middleware(l, "/api", true, inner)

	r := httptest.NewRequest(http.MethodGet, "/api?q=1", nil)
	r.RemoteAddr = "203.0.113.9:51234"
	r.SetBasicAuth("frank", "pw")
	h.ServeHTTP(httptest.NewRecorder(), r)

	r = httptest.NewRequest(http.MethodGet, "/fail", nil)
	r.RemoteAddr = "203.0.113.9:51234"
	h.ServeHTTP(httptest.NewRecorder(), r)

	l.Close()

	access := readFile(t, o.Filename)
	lines := strings.Split(strings.TrimSpace(access), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 access log lines, got %d: %q", len(lines), access)
	}
	expected := `203.0.113.9 frank "GET /api?q=1 HTTP/1.1" 200 5 hit example1 rp /api`
	if lines[0] != expected {
		t.Errorf("expected %q, got %q", expected, lines[0])
	}
	if !strings.Contains(lines[1], " 502 14 ") {
		t.Errorf("unexpected error request line: %q", lines[1])
	}

	// only the 502 goes to the error log
	errLog := readFile(t, o.ErrorFilename)
	errLines := strings.Split(strings.TrimSpace(errLog), "\n")
	if len(errLines) != 1 || !strings.Contains(errLines[0], " 502 ") {
		t.Errorf("unexpected error log content: %q", errLog)
	}
}

func TestMiddlewareAccessOnly(t *testing.T) {
	o := testLoggerOptions(t)
	o.ErrorFilename = ""
	o.Format = "%m %s"
	l, err := NewLogger(o, 0, "b", "p")
	if err != nil {
		t.Fatal(err)
	}
	h := Middleware(l, "/", false, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if request.GetResources(r) != nil {
				t.Error("access logging seeded resources without an authenticator")
			}
			w.WriteHeader(http.StatusNotFound)
		}))
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodDelete, "/x", nil))
	l.Close()
	if s := readFile(t, o.Filename); s != "DELETE 404\n" {
		t.Errorf("unexpected access log: %q", s)
	}
}

func TestMiddlewareErrorOnlyWithThreshold(t *testing.T) {
	o := testLoggerOptions(t)
	o.Filename = ""
	o.Format = "%m %s"
	o.ErrorThreshold = 500
	l, err := NewLogger(o, 0, "b", "p")
	if err != nil {
		t.Fatal(err)
	}
	h := Middleware(l, "/", false, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/404" {
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/404", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/500", nil))
	l.Close()
	// 404 is below the 500 threshold, so only the 500 is logged
	if s := readFile(t, o.ErrorFilename); s != "GET 500\n" {
		t.Errorf("unexpected error log: %q", s)
	}
}

func TestMiddlewareNilLogger(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})
	h := Middleware(nil, "/", false, inner)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("expected next handler to be called")
	}
}

func TestMiddlewareUsesAuthenticatedUsername(t *testing.T) {
	o := testLoggerOptions(t)
	o.ErrorFilename = ""
	o.Format = "%u"
	l, err := NewLogger(o, 0, "b", "p")
	if err != nil {
		t.Fatal(err)
	}
	h := Middleware(l, "/", true, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/verified" {
				request.GetResources(r).AuthResult = &authtypes.AuthResult{
					Status: authtypes.AuthSuccess, Username: "verified"}
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	unverified := httptest.NewRequest(http.MethodGet, "/unverified", nil)
	unverified.SetBasicAuth("asserted", "wrong")
	h.ServeHTTP(httptest.NewRecorder(), unverified)
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/verified", nil))
	l.Close()
	if s := readFile(t, o.Filename); s != "-\nverified\n" {
		t.Errorf("unexpected authenticated users: %q", s)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushRecorder) Flush() {
	r.flushed = true
}

func TestRecorderUnwrap(t *testing.T) {
	base := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec := utilmiddleware.NewResponseObserver(base)
	if err := http.NewResponseController(rec).Flush(); err != nil {
		t.Fatal(err)
	}
	if !base.flushed {
		t.Error("expected flush to reach the underlying writer")
	}
	rec2 := utilmiddleware.NewResponseObserver(httptest.NewRecorder())
	rec2.WriteHeader(http.StatusEarlyHints)
	rec2.WriteHeader(http.StatusCreated)
	rec2.WriteHeader(http.StatusInternalServerError)
	if rec2.StatusCode() != http.StatusCreated {
		t.Errorf("recorded status = %d, want %d", rec2.StatusCode(), http.StatusCreated)
	}
}

func TestParseResultHeader(t *testing.T) {
	result := headers.ParseResultHeader(
		"engine=DeltaProxyCache; status=phit; fetched=[1-2]; ffstatus=hit")
	if result.Engine != "DeltaProxyCache" || result.Status != "phit" {
		t.Errorf("unexpected parse result: %s, %s", result.Engine, result.Status)
	}
	result = headers.ParseResultHeader("")
	if result.Engine != "" || result.Status != "" {
		t.Errorf("expected empty results, got %s, %s", result.Engine, result.Status)
	}
	result = headers.ParseResultHeader("junk; status=hit")
	if result.Engine != "" || result.Status != "hit" {
		t.Errorf("unexpected parse result: %s, %s", result.Engine, result.Status)
	}
}

func TestClientIPAndSplitAddr(t *testing.T) {
	if ip := clientIP("203.0.113.9:1234"); ip != "203.0.113.9" {
		t.Errorf("unexpected ip: %s", ip)
	}
	if ip := clientIP("203.0.113.9"); ip != "203.0.113.9" {
		t.Errorf("unexpected ip: %s", ip)
	}
	if ip := clientIP("[::1]:1234"); ip != "::1" {
		t.Errorf("unexpected ip: %s", ip)
	}
	host, port := splitAddr("10.0.0.1:8480")
	if host != "10.0.0.1" || port != "8480" {
		t.Errorf("unexpected split: %s, %s", host, port)
	}
	host, port = splitAddr("pipe")
	if host != "pipe" || port != "" {
		t.Errorf("unexpected split: %s, %s", host, port)
	}
}

func TestLoggerClose(t *testing.T) {
	var nilLogger *Logger
	nilLogger.Close() // must not panic
	o := testLoggerOptions(t)
	l, err := NewLogger(o, 0, "b", "p")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	l.Close() // idempotent
}

func waitClosed(t *testing.T, l *Logger) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := l.access.Write(nil); err != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("expected logger to be closed")
}

func TestGenerations(t *testing.T) {
	dir := t.TempDir()
	newGenLogger := func(name string) *Logger {
		l, err := NewLogger(&alo.Options{
			Filename: filepath.Join(dir, name)}, 0, "b", "p")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	BeginGeneration()
	l1 := newGenLogger("gen1.access.log")
	// simulate a reload: l1 moves to previous, l2 is the new generation
	BeginGeneration()
	l2 := newGenLogger("gen2.access.log")
	CommitGeneration(0)
	waitClosed(t, l1)
	if _, err := l2.access.Write([]byte("still alive\n")); err != nil {
		t.Errorf("expected current generation to remain open: %v", err)
	}

	// abort closes the failed generation and restores the previous one
	BeginGeneration()
	l3 := newGenLogger("gen3.access.log")
	AbortGeneration()
	if _, err := l3.access.Write(nil); err == nil {
		t.Error("expected aborted generation's logger to be closed")
	}
	if _, err := l2.access.Write([]byte("survivor\n")); err != nil {
		t.Errorf("expected restored generation to remain open: %v", err)
	}
	BeginGeneration()
	CommitGeneration(0)
	waitClosed(t, l2)
}
