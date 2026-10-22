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

package moq

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize"
	pno "github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func denyAllList(t *testing.T) *ipacl.List {
	t.Helper()
	// a list with no entries takes its default verdict, deny, for every address
	list, _, err := ipacl.Compile(ipacl.Options{Name: "deny-all"})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func closedWith(t *testing.T, s *session.Session) uint64 {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("the session stays open")
	}
	return refusedWith(t, s, s.Err())
}

func reloaded(clients ...*Client) native.BuildRequest {
	// the build request a reload hands the listener, from the clients' own options
	c := config.NewConfig()
	c.Listeners["moq1"] = &listener.Options{Protocol: listener.ProtocolMoQ, ListenAddress: "127.0.0.1"}
	c.Backends = bo.Lookup{}
	all := backends.Backends{}
	for _, cl := range clients {
		c.Backends[cl.Name()] = cl.Configuration()
		all[cl.Name()] = cl
	}
	return native.BuildRequest{Config: c, ListenerName: "moq1", Listener: c.Listeners["moq1"], BackendClients: all}
}

func TestReloadJudgesOpenSessions(t *testing.T) {
	org := startOrigin(t)
	c := newBackend(t, "rejudged", "moqt://"+org.addr+"/live")
	st := startListener(t, mo.NewListener(), c)
	// a listener list a reload swaps in closes the open sessions it refuses
	s, err := dial(t, st.addr, "x")
	if err != nil {
		t.Fatal(err)
	}
	// a request answered shows the server's side of the session began, and its close is a session's
	trackStatus(t, s)
	st.srv.UpdateIPACL(denyAllList(t))
	if code := closedWith(t, s); code != uint64(message.CodeUnauthorized) {
		t.Errorf("closed with %#x", code)
	}
	st.srv.UpdateIPACL(nil)
	// as does the new list of the backend a session was routed to
	if s, err = dial(t, st.addr, "x"); err != nil {
		t.Fatal(err)
	}
	trackStatus(t, s)
	o := c.Configuration()
	o.IPACLName, o.IPACL = "deny-all", denyAllList(t)
	st.srv.UpdateRouteResolver(routesFor(reloaded(c)))
	if code := closedWith(t, s); code != uint64(message.CodeUnauthorized) {
		t.Errorf("closed with %#x", code)
	}
}

func TestReloadJudgesWebTransportSessions(t *testing.T) {
	org := startOrigin(t)
	c := newBackend(t, "wt-rejudged", "moqt://"+org.addr+"/live")
	addr := startWTEndpoint(t, http.HandlerFunc(c.serveWebTransport))
	_, s, err := dialWT(t, addr, nil, alpn21)
	if err != nil {
		t.Fatal(err)
	}
	// the session is tracked once it starts serving
	eventually(t, "the session to be tracked", func() bool {
		c.sh.mu.Lock()
		defer c.sh.mu.Unlock()
		return len(c.sh.wt) == 1
	})
	c.sh.rejudgeWT(sessionJudge{})
	select {
	case <-s.Done():
		t.Fatal("a session the new lists admit is closed")
	case <-time.After(50 * time.Millisecond):
	}
	// a reload's commit judges the open sessions by the backend's new lists
	o := c.Configuration()
	o.GeoACLName, o.GeoACLOptions = "test-geo", &geoaclopts.Options{Compiled: compiledGeo{denyAll{}}}
	RetireClients(backends.Backends{c.Name(): c})
	if code := closedWith(t, s); code != uint64(message.CodeUnauthorized) {
		t.Errorf("closed with %#x", code)
	}
}

func TestListenerACLBeforeHandshake(t *testing.T) {
	org := startOrigin(t)
	c := newBackend(t, "fenced", "moqt://"+org.addr+"/live")
	name := "fence-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	st := startListenerWith(t, mo.NewListener(), func(o *listener.Options) {
		o.IPACLName, o.IPACL = name, denyAllList(t)
	}, c)
	denied := metrics.IPACLDecisions.WithLabelValues(name, metrics.IPACLScopeListener, "deny")
	if _, err := dial(t, st.addr, "x"); err == nil {
		t.Error("a listed client is refused")
	}
	if n := testutil.ToFloat64(denied); n != 1 {
		t.Errorf("%v refusals counted", n)
	}
	if st.srv.active.Load() != 0 {
		t.Error("a refused client never reaches the session limit")
	}
}

func TestSessionRecords(t *testing.T) {
	org := startOrigin(t)
	logPath := filepath.Join(t.TempDir(), "access.log")
	c := newBackendWith(t, "recorded", "moqt://"+org.addr+"/live", func(o *bo.Options) {
		o.AccessLog = &alo.Options{Filename: logPath, Format: "%m %U %H %v"}
		o.TracingConfigName = "spans"
	})
	st := startListener(t, mo.NewListener(), c)
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	req := reloaded(c)
	req.Tracers = tracing.Tracers{"spans": &tracing.Tracer{Tracer: tp.Tracer("moq"), Name: "spans"}}
	st.srv.UpdateRouteResolver(routesFor(req))
	s, err := dial(t, st.addr, "relay.example")
	if err != nil {
		t.Fatal(err)
	}
	// a request answered shows the server's side of the session began
	trackStatus(t, s)
	_ = s.Close(message.CodeNoError, "")
	// each native session leaves one access log line and one span
	var logged string
	defer func() {
		if t.Failed() {
			t.Logf("access log: %q", logged)
		}
	}()
	eventually(t, "the access log line", func() bool {
		b, _ := os.ReadFile(logPath)
		logged = string(b)
		return strings.Contains(logged, "SETUP / moqt-21 relay.example")
	})
	eventually(t, "the session span", func() bool {
		for _, sp := range spans.Ended() {
			if sp.Name() == spanSession {
				return true
			}
		}
		return false
	})
}

func TestNormalizedSetupPath(t *testing.T) {
	rt := &routeTable{normalizer: normalize.New(&pno.Options{MergeSlashes: true, DotSegments: pno.DotSegmentsReject})}
	if p, ok := rt.normalized("//live//sports"); !ok || p != "/live/sports" {
		t.Errorf("merged %q %v", p, ok)
	}
	if _, ok := rt.normalized("/a/../live"); ok {
		t.Error("a path the listener refuses is refused")
	}
	if p, ok := rt.normalized("relative"); !ok || p != "relative" {
		t.Error("a path not starting at the root is routed as it is")
	}
	if p, ok := (&routeTable{}).normalized("//x"); !ok || p != "//x" {
		t.Error("a listener that normalizes nothing changes nothing")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthHandlerRegistered(t *testing.T) {
	// the backend's health path on the metrics listener reports its origin probe, as any backend's does
	c := newBackend(t, "healthy", "moqt://127.0.0.1:1/live")
	if _, ok := c.Handlers()[handlerHealth]; !ok {
		t.Error("a moq backend registers a health handler")
	}
}
