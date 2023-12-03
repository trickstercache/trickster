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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/router"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func testListener() net.Listener {
	l, _ := net.Listen("tcp", fmt.Sprintf("%s:%d", "", 0))
	return l
}

func TestListeners(t *testing.T) {
	tr, _ := stdout.New(nil)
	tr.ShutdownFunc = func(_ context.Context) error { return nil }
	trs := map[string]*tracing.Tracer{"default": tr}
	testLG := NewListenerGroup()

	wg := &sync.WaitGroup{}
	var err error
	wg.Add(1)
	go func() {

		tc := &tls.Config{
			Certificates: make([]tls.Certificate, 1),
		}

		err = testLG.StartListener("httpListener",
			"", 0, 20, tc, http.NewServeMux(), wg, trs, nil, 0, tl.ConsoleLogger("info"))
	}()

	time.Sleep(time.Millisecond * 300)
	l := testLG.members["httpListener"]
	l.Close()
	time.Sleep(time.Millisecond * 100)
	if err == nil {
		t.Error("expected non-nil err")
	}

	wg.Add(1)
	go func() {
		err = testLG.StartListenerRouter("httpListener2",
			"", 0, 20, nil, "/", http.HandlerFunc(ph.HandleLocalResponse), wg,
			nil, nil, 0, tl.ConsoleLogger("info"))
	}()
	time.Sleep(time.Millisecond * 300)
	l = testLG.members["httpListener2"]
	l.Listener.Close()
	time.Sleep(time.Millisecond * 100)
	if err == nil {
		t.Error("expected non-nil err")
	}

	wg.Add(1)
	err = testLG.StartListener("testBadPort",
		"", -31, 20, nil, http.NewServeMux(), wg, trs, nil, 0, tl.ConsoleLogger("info"))
	if err == nil {
		t.Error("expected invalid port error")
	}
	wg.Wait()
}

func TestUpdateRouter(t *testing.T) {
	testLG := NewListenerGroup()
	testLG.members["test"] = &Listener{routeSwapper: &ph.SwitchHandler{}}
	r := http.NewServeMux()
	testLG.UpdateRouter("test", r)
	if testLG.members["test"].routeSwapper.Handler() != r {
		t.Error("router mismatch")
	}
}

func TestNewListenerErr(t *testing.T) {
	config.NewConfig()
	l, err := NewListener("-", 0, 0, nil, 0, tl.ConsoleLogger("error"))
	if err == nil {
		l.Close()
		t.Errorf("expected error: %s", `listen tcp: lookup -: no such host`)
	}
}

func TestListenerAccept(t *testing.T) {
	testLG := NewListenerGroup()
	var err error
	go func() {
		err = testLG.StartListener("httpListener",
			"", 0, 20, nil, http.NewServeMux(), nil, nil, nil, 0, tl.ConsoleLogger("info"))
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

	l, err := NewListener("", 0, 0, tlsConfig, 0, tl.ConsoleLogger("error"))
	if err != nil {
		t.Error(err)
	} else {
		defer l.Close()
	}

}

func TestListenerConnectionLimitWorks(t *testing.T) {

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "hello!")
	}
	es := httptest.NewServer(http.HandlerFunc(handler))
	defer es.Close()

	_, _, err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-provider", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	tt := []struct {
		Name             string
		ListenPort       int
		ConnectionsLimit int
		Clients          int
		expectedErr      string
	}{
		{
			"Without connection limit",
			34001,
			0,
			1,
			"",
		},
		{
			"With connection limit of 10",
			34002,
			10,
			10,
			"",
		},
		{
			"With connection limit of 1, but with 10 clients",
			34003,
			1,
			10,
			"(Client.Timeout exceeded while awaiting headers)",
		},
	}

	http.DefaultClient.Timeout = 100 * time.Millisecond

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			l, err := NewListener("", tc.ListenPort, tc.ConnectionsLimit, nil, 0, tl.ConsoleLogger("error"))
			if err != nil {
				t.Fatal(err)
			} else {
				defer l.Close()
			}

			go func() {
				http.Serve(l, router.NewRouter())
			}()

			if err != nil {
				t.Fatalf("failed to create listener: %s", err)
			}

			for i := 0; i < tc.Clients; i++ {
				r, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d/", tc.ListenPort), nil)
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
	lg := NewListenerGroup()
	lg.members["testing"] = &Listener{exitOnError: true}
	l := lg.Get("testing")
	if !l.exitOnError {
		t.Error("expected true")
	}
	l = lg.Get("invalid")
	if l != nil {
		t.Error("expected nil")
	}
}

func TestDrainAndClose(t *testing.T) {
	l := &Listener{Listener: testListener(), server: &http.Server{}}
	lg := NewListenerGroup()
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
		routeSwapper: ph.NewSwitchHandler(nil),
	}
	lg := NewListenerGroup()
	lg.members["httpListener"] = l
	lg.members["reloadListener"] = l
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
		TCPConn: tconn,
	}
	err = oconn.Close()
	if err != nil {
		t.Error(err)
	}
}
