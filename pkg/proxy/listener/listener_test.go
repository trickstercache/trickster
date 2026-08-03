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

package listener

import (
	"context"
	"crypto/tls"
	stderrors "errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/local"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/switcher"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	dto "github.com/prometheus/client_model/go"
	"golang.org/x/net/netutil"
)

func testListener() net.Listener {
	l, _ := net.Listen("tcp", fmt.Sprintf("%s:%d", "", 0))
	return l
}

func TestListeners(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	tr, _ := stdout.New(nil)
	tr.ShutdownFunc = func(_ context.Context) error { return nil }
	trs := tracing.Tracers{"default": tr}
	testLG := NewGroup()

	var err error
	errs := make(chan error, 1)
	go func() {
		tc := &tls.Config{
			Certificates: make([]tls.Certificate, 1),
		}
		errs <- testLG.StartListener("httpListener",
			"", 0, 20, tc, http.NewServeMux(), trs, nil, 0, 0)
		close(errs)
	}()

	time.Sleep(time.Millisecond * 300)
	testLG.listenersLock.Lock()
	l := testLG.members["httpListener"]
	l.Close()
	testLG.listenersLock.Unlock()
	time.Sleep(time.Millisecond * 100)
	err = <-errs
	if !stderrors.Is(err, net.ErrClosed) {
		t.Error(err, "expected nil err")
	}
	errs2 := make(chan error, 1)
	go func() {
		errs2 <- testLG.StartListenerRouter("httpListener2",
			"", 0, 20, nil, "/", http.HandlerFunc(local.HandleLocalResponse),
			nil, nil, 0, 0)
		close(errs2)
	}()
	time.Sleep(time.Millisecond * 300)
	testLG.listenersLock.Lock()
	l = testLG.members["httpListener2"]
	l.Listener.Close()
	testLG.listenersLock.Unlock()
	time.Sleep(time.Millisecond * 100)
	err = <-errs2
	if !stderrors.Is(err, net.ErrClosed) {
		t.Error(err, "expected nil err")
	}

	err = testLG.StartListener("testBadPort",
		"", -31, 20, nil, http.NewServeMux(), trs, nil, 0, 0)
	if err == nil {
		t.Error("expected invalid port error")
	}
}

func TestUpdateRouter(t *testing.T) {
	testLG := NewGroup()
	testLG.members["test"] = &Listener{routeSwapper: &switcher.SwitchHandler{}}
	r := http.NewServeMux()
	testLG.UpdateRouter("test", r)
	if testLG.members["test"].routeSwapper.Handler() != r {
		t.Error("router mismatch")
	}
}

func TestNewListenerErr(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	config.NewConfig()
	l, err := NewListener("-", 0, 0, nil, 0)
	if err == nil {
		l.Close()
		t.Errorf("expected error: %s", `listen tcp: lookup -: no such host`)
	}
}

func TestListenerAccept(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	testLG := NewGroup()
	var err error
	go func() {
		err = testLG.StartListener("httpListener",
			"", 0, 20, nil, http.NewServeMux(), nil, nil, 0, 0)
	}()
	time.Sleep(time.Millisecond * 500)
	if err != nil {
		t.Error(err)
	}
	l := testLG.Get("httpListener")
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Error(err)
	}
	conn.Close()
	l.Close()
}

func TestNewListenerTLS(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	c := config.NewConfig()
	o := c.Backends["default"]
	c.Frontend.ServeTLS = true

	tc := o.TLS
	o.TLS.ServeTLS = true

	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if err != nil {
		t.Error(err)
	}
	if closer != nil {
		defer closer()
	}

	tc.FullChainCertPath = cf
	tc.PrivateKeyPath = kf

	tlsConfig, err := c.TLSCertConfig()
	if err != nil {
		t.Error(err)
	}

	l, err := NewListener("", 0, 0, tlsConfig, 0)
	if err != nil {
		t.Error(err)
	} else {
		defer l.Close()
	}
}

func TestListenerConnectionLimitWorks(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "hello!")
	}
	es := httptest.NewServer(http.HandlerFunc(handler))
	defer es.Close()

	_, err := config.Load([]string{"-origin-url", es.URL, "-provider", providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	tt := []struct {
		Name             string
		ConnectionsLimit int
		Clients          int
		expectedErr      string
	}{
		{
			"Without connection limit",
			0,
			1,
			"",
		},
		{
			"With connection limit of 10",
			10,
			10,
			"",
		},
		{
			"With connection limit of 1, but with 10 clients",
			1,
			10,
			"(Client.Timeout exceeded while awaiting headers)",
		},
	}

	http.DefaultClient.Timeout = 100 * time.Millisecond

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			// Bind to port 0 so the kernel picks a free ephemeral port;
			// fixed ports flake on shared CI runners when the prior
			// subtest's socket lingers in TIME_WAIT.
			l, err := NewListener("", 0, tc.ConnectionsLimit, nil, 0)
			if err != nil {
				t.Fatal(err)
			} else {
				defer l.Close()
			}
			port := l.Addr().(*net.TCPAddr).Port
			go func() {
				http.Serve(l, lm.NewRouter())
			}()

			// poll until listener is up
			for {
				conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
				if err == nil {
					conn.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}

			for i := 0; i < tc.Clients; i++ {
				r, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d/", port), nil)
				if err != nil {
					t.Fatalf("failed to create request: %s", err)
				}
				res, err := http.DefaultClient.Do(r)
				if err != nil {
					if !strings.HasSuffix(err.Error(), tc.expectedErr) {
						t.Fatalf("unexpected error when executing request: %s", err)
					}
					continue
				}
				defer func() {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}()
			}
		})
	}
}

func TestCertSwapper(t *testing.T) {
	l := &Listener{}
	cs := l.CertSwapper()
	if cs != nil {
		t.Error("expected nil cert swapper")
	}
}

func TestRouteSwapper(t *testing.T) {
	l := &Listener{}
	rs := l.RouteSwapper()
	if rs != nil {
		t.Error("expected nil route swapper")
	}
}

func TestGet(t *testing.T) {
	lg := NewGroup()
	l0 := &Listener{}
	l0.exitOnError.Store(true)
	lg.members["testing"] = l0
	l := lg.Get("testing")
	if !l.exitOnError.Load() {
		t.Error("expected true")
	}
	l = lg.Get("invalid")
	if l != nil {
		t.Error("expected nil")
	}
}

func TestDrainAndClose(t *testing.T) {
	l := &Listener{Listener: testListener(), server: &http.Server{}}
	lg := NewGroup()
	lg.members["testing"] = l
	err := lg.DrainAndClose("testing", 0)
	if err != nil {
		t.Error(err)
	}
	lg.members["nilListener"] = &Listener{}
	err = lg.DrainAndClose("nilListener", 0)
	if err != errors.ErrNilListener {
		t.Error("expected error for nil listener")
	}
	err = lg.DrainAndClose("unknown", 0)
	if err != errors.ErrNoSuchListener {
		t.Error("expected error for no such listener")
	}
}

func TestUpdateRouters(t *testing.T) {
	testRouter := http.NotFoundHandler()
	l := &Listener{
		Listener:     testListener(),
		routeSwapper: switcher.NewSwitchHandler(nil),
	}
	lg := NewGroup()
	lg.members["httpListener"] = l
	lg.members["mgmtListener"] = l
	lg.UpdateFrontendRouters(testRouter, testRouter)
	if l.RouteSwapper() == nil {
		t.Error("expected non-nil swapper")
	}
	if l.routeSwapper.Handler() == nil {
		t.Error("expected non-nil handler")
	}
}

func TestCloseObservedConnection(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(testutil.BasicHTTPHandler))
	defer s.Close()
	address := s.URL[7:]
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Errorf("invalid address:[%s]", address)
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Error(err)
	}
	tconn, ok := conn.(*net.TCPConn)
	if !ok {
		t.Error("invalid connection type")
	}
	oconn := &observedConnection{
		Conn: tconn,
	}
	err = oconn.Close()
	if err != nil {
		t.Error(err)
	}
}

func TestObservedConnectionIdempotentClose(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(testutil.BasicHTTPHandler))
	defer s.Close()
	address := s.URL[7:]
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	tconn, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatal("invalid connection type")
	}
	oconn := &observedConnection{
		Conn: tconn,
	}

	metrics.ProxyActiveConnections.Set(10.0)

	// First Close succeeds and decrements the gauge exactly once.
	if err := oconn.Close(); err != nil {
		t.Errorf("first Close: unexpected error: %v", err)
	}

	// Subsequent Closes return an error because the conn is already closed;
	// that error is what prevents the gauge from being decremented again.
	for range 5 {
		if err := oconn.Close(); err == nil {
			t.Error("expected an error closing an already-closed connection")
		}
	}

	var m dto.Metric
	metrics.ProxyActiveConnections.Write(&m)
	finalVal := m.GetGauge().GetValue()

	if finalVal != 9.0 {
		t.Errorf("expected ProxyActiveConnections metric to be 9.0 after idempotent close, got %f", finalVal)
	}
}

func TestListenerState(t *testing.T) {
	l := &Listener{}
	if l.State() != StateStopped {
		t.Errorf("State() = %v, want %v", l.State(), StateStopped)
	}
	l.setState(StateReady)
	if l.State() != StateReady {
		t.Errorf("State() = %v, want %v", l.State(), StateReady)
	}
}

func TestListenerWaitForReadyNilChannel(t *testing.T) {
	l := &Listener{}
	if l.WaitForReady(0) {
		t.Error("expected false when state is not ready and readyCh is nil")
	}
	l.setState(StateReady)
	if !l.WaitForReady(0) {
		t.Error("expected true when state is ready and readyCh is nil")
	}
}

func TestListenerWaitForReadyBlocksUntilReady(t *testing.T) {
	l := &Listener{readyCh: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		l.markReady()
	}()
	done := make(chan bool, 1)
	go func() { done <- l.WaitForReady(0) }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("expected WaitForReady(0) to return true once ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForReady(0) did not return after the listener became ready")
	}
}

func TestListenerWaitForReadyTimeoutSuccess(t *testing.T) {
	l := &Listener{readyCh: make(chan struct{})}
	l.markReady()
	if !l.WaitForReady(time.Second) {
		t.Error("expected true when already ready before the timeout")
	}
}

func TestListenerWaitForReadyTimeoutExpires(t *testing.T) {
	l := &Listener{readyCh: make(chan struct{})}
	t.Cleanup(l.markReady)
	if l.WaitForReady(20 * time.Millisecond) {
		t.Error("expected false when the listener never becomes ready")
	}
}

func TestGroupWaitForReadyEmpty(t *testing.T) {
	lg := NewGroup()
	if err := lg.WaitForReady(time.Second); err != nil {
		t.Errorf("err = %v, want nil for an empty group", err)
	}
}

func TestGroupWaitForReadySkipsNilMembers(t *testing.T) {
	lg := NewGroup()
	lg.members["nil-member"] = nil
	l := &Listener{readyCh: make(chan struct{})}
	l.markReady()
	lg.members["ready-member"] = l
	if err := lg.WaitForReady(time.Second); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestGroupWaitForReadyNoTimeoutBlocksUntilReady(t *testing.T) {
	lg := NewGroup()
	l := &Listener{readyCh: make(chan struct{})}
	lg.members["member"] = l
	go func() {
		time.Sleep(20 * time.Millisecond)
		l.markReady()
	}()
	done := make(chan error, 1)
	go func() { done <- lg.WaitForReady(0) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForReady(0) did not return after the listener became ready")
	}
}

func TestGroupWaitForReadyTimeoutExpires(t *testing.T) {
	lg := NewGroup()
	l := &Listener{readyCh: make(chan struct{})}
	t.Cleanup(l.markReady)
	lg.members["never-ready"] = l
	if err := lg.WaitForReady(20 * time.Millisecond); err == nil {
		t.Error("expected a timeout error when a listener never becomes ready")
	}
}

func TestGroupShutdownAggregatesErrorsAndClosesDone(t *testing.T) {
	lg := NewGroup()
	lg.members["nil-listener"] = &Listener{}
	lg.members["ok-listener"] = &Listener{Listener: testListener(), server: &http.Server{}}

	if err := lg.Shutdown(0); !stderrors.Is(err, errors.ErrNilListener) {
		t.Errorf("err = %v, want %v", err, errors.ErrNilListener)
	}
	select {
	case <-lg.done:
	default:
		t.Error("expected the done channel to be closed")
	}

	// Shutdown must be idempotent: a second call must not panic closing an
	// already-closed done channel, and an empty group shuts down cleanly.
	if err := lg.Shutdown(0); err != nil {
		t.Errorf("second Shutdown err = %v, want nil", err)
	}
}

func TestDrainAndCloseServerShutdownError(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	inHandler := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(inHandler)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	lg := NewGroup()
	errs := make(chan error, 1)
	go func() {
		errs <- lg.StartListener("blocking", "127.0.0.1", 0, 0, nil, handler, nil, nil, 0, 0)
	}()

	var l *Listener
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l = lg.Get("blocking"); l != nil && l.State() == StateReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l == nil {
		t.Fatal("listener did not become ready")
	}

	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/", l.Addr().String()))
		if err == nil {
			resp.Body.Close()
		}
	}()

	select {
	case <-inHandler:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}

	// A drainWait of 0 means the shutdown context is already expired. With
	// an in-flight (non-idle) connection, Shutdown must observe ctx.Done()
	// rather than waiting indefinitely for the handler to finish.
	if err := lg.DrainAndClose("blocking", 0); err == nil {
		t.Error("expected an error from a shutdown that outlives its drain deadline")
	}

	close(release)
	if err := <-errs; err != nil && !stderrors.Is(err, http.ErrServerClosed) {
		t.Errorf("StartListener returned unexpected error: %v", err)
	}
}

func TestHandleTracerShutdowns(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	var called bool
	trs := tracing.Tracers{
		"nil-tracer":        nil,
		"nil-shutdown-func": {},
		"errors": {
			ShutdownFunc: func(context.Context) error {
				called = true
				return stderrors.New("shutdown failed")
			},
		},
	}
	handleTracerShutdowns(trs)
	if !called {
		t.Error("expected the tracer's ShutdownFunc to be invoked")
	}
}

func TestAcceptUnwrapsTLSConn(t *testing.T) {
	keyPEM, certPEM, err := tlstest.GetTestKeyAndCert(false)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	l := &Listener{Listener: raw}

	go func() {
		if c, derr := net.Dial("tcp", raw.Addr().String()); derr == nil {
			defer c.Close()
			// hold the raw connection open briefly; a completed handshake
			// is not required for the server side to hand back a *tls.Conn
			time.Sleep(100 * time.Millisecond)
		}
	}()

	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.(*tls.Conn); !ok {
		t.Errorf("Accept did not return a *tls.Conn unwrapped: got %T", conn)
	}
}

func TestStartListenerCallsFOnBindFailure(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	var called bool
	lg := NewGroup()
	err := lg.StartListener("testBadPort", "", -31, 0, nil, http.NewServeMux(),
		nil, func() { called = true }, 0, 0)
	if err == nil {
		t.Error("expected an error for an invalid port")
	}
	if !called {
		t.Error("expected f to be called when the listener fails to bind")
	}
}

// TestStartListenerExitOnServeError verifies that a listener created with a
// non-nil f treats a post-startup Serve() failure as fatal to the whole
// process, for both the HTTP and HTTPS code paths. This calls os.Exit(1)
// directly, so it must run in a subprocess.
func TestStartListenerExitOnServeError(t *testing.T) {
	scenarios := []struct {
		name string
		tls  bool
	}{
		{"http", false},
		{"https", true},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			if os.Getenv("LISTENER_EXIT_TEST") == sc.name {
				runExitOnServeErrorChild(sc.tls)
				return
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestStartListenerExitOnServeError/"+sc.name+"$", "-test.v")
			cmd.Env = append(os.Environ(), "LISTENER_EXIT_TEST="+sc.name)
			err := cmd.Run()
			var ee *exec.ExitError
			if !stderrors.As(err, &ee) {
				t.Fatalf("expected an ExitError, got %v", err)
			}
			if ee.ExitCode() != 1 {
				t.Fatalf("exit code = %d, want 1", ee.ExitCode())
			}
		})
	}
}

// runExitOnServeErrorChild starts a listener with a non-nil f (so
// exitOnError is set), then forces Serve() to fail by closing the raw
// socket out from under it. StartListener should then call os.Exit(1)
// itself; if it doesn't within the deadline, exit non-zero so the parent's
// exit-code assertion fails loudly instead of hanging.
func runExitOnServeErrorChild(useTLS bool) {
	logger.SetLogger(logging.NoopLogger())
	lg := NewGroup()
	var tc *tls.Config
	if useTLS {
		tc = &tls.Config{Certificates: make([]tls.Certificate, 1)}
	}
	go func() {
		_ = lg.StartListener("child", "", 0, 0, tc, http.NewServeMux(), nil,
			func() {}, 0, 0)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := lg.Get("child"); l != nil && l.State() == StateReady {
			l.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(2 * time.Second)
	os.Exit(2)
}

func TestAcceptWrapsLimitListenerConn(t *testing.T) {
	// With connections_limit set, the listener is wrapped by a
	// netutil.LimitListener whose conns are not *net.TCPConn. Accept must
	// still wrap them so Close decrements the active-connections gauge.
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	l := &Listener{Listener: netutil.LimitListener(raw, 10)}

	go func() {
		if c, derr := net.Dial("tcp", raw.Addr().String()); derr == nil {
			c.Close()
		}
	}()

	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.(*observedConnection); !ok {
		t.Errorf("Accept did not wrap LimitListener conn: got %T, want *observedConnection", conn)
	}
}
