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

package server

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/handler"
	aclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	aclhandler "github.com/trickstercache/trickster/v2/pkg/proxy/ipacl/handler"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
)

const geoRefusal = "This resource is not available in your geographical area."

var _ listener.SessionGateUpdater = (*Server)(nil)

type testGate struct {
	refuse atomic.Bool
	last   atomic.Pointer[netip.Addr]
}

func (g *testGate) Admit(client netip.Addr) *backends.Denial {
	g.last.Store(&client)
	if g.refuse.Load() {
		return &backends.Denial{Reason: backends.DenialLocation, Message: geoRefusal}
	}
	return nil
}

func TestSessionGate(t *testing.T) {
	s := New(echoJSONHandler(), nil, false, "gate")
	gate := &testGate{}
	gate.refuse.Store(true)
	s.UpdateSessionGate(gate)
	address := startTestProtocolServer(t, s)
	query := func() error {
		db := chdriver.OpenDB(&chdriver.Options{Addr: []string{address}})
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var got string
		return db.QueryRowContext(ctx, "SELECT 1").Scan(&got)
	}

	// a refused session gets IP_ADDRESS_NOT_ALLOWED with the message, before the server's hello
	err := query()
	var exception *chdriver.Exception
	if !errors.As(err, &exception) || exception.Code != exceptionIPAddressNotAllowed ||
		exception.Message != geoRefusal {
		t.Fatalf("refused session: %v", err)
	}
	if got := *gate.last.Load(); got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("judged %v", got)
	}

	gate.refuse.Store(false)
	if err := query(); err != nil {
		t.Fatalf("admitted session: %v", err)
	}
	gate.refuse.Store(true)
	s.UpdateSessionGate(nil)
	if err := query(); err != nil {
		t.Fatalf("session after the gate was removed: %v", err)
	}
}

func TestQueriesJudgedAfterAdmission(t *testing.T) {
	const name = "ch-per-query"
	loc, err := geofeed.New(name, &geofeedopts.Options{Entries: []string{"127.0.0.1/32,FR"}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := acl.Compile(&aclopts.Options{Name: name, Deny: []string{"FR"}, Message: geoRefusal}, loc, name)
	if err != nil {
		t.Fatal(err)
	}
	// stands in for a reload that puts the geo ACL on the backend router every query is sent through
	var judged atomic.Bool
	echo, gated := echoJSONHandler(), handler.New(a, echoJSONHandler())
	s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if judged.Load() {
			gated.ServeHTTP(w, r)
			return
		}
		echo.ServeHTTP(w, r)
	}), nil, false, "per-query")
	gate := &testGate{}
	s.UpdateSessionGate(gate)
	address := startTestProtocolServer(t, s)

	db := chdriver.OpenDB(&chdriver.Options{Addr: []string{address}})
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got string
	if err := conn.QueryRowContext(ctx, "SELECT 1").Scan(&got); err != nil {
		t.Fatalf("admitted session: %v", err)
	}

	judged.Store(true)
	gate.refuse.Store(true)
	// the open session is not judged again, but its next query is, and is refused as a failed query
	err = conn.QueryRowContext(ctx, "SELECT 1").Scan(&got)
	var exception *chdriver.Exception
	if !errors.As(err, &exception) || exception.Code != 62 || !strings.Contains(exception.Message, geoRefusal) {
		t.Fatalf("query on the admitted session: %v", err)
	}
	// a new session is refused before the server's hello
	other := chdriver.OpenDB(&chdriver.Options{Addr: []string{address}})
	defer other.Close()
	err = other.PingContext(ctx)
	if !errors.As(err, &exception) || exception.Code != exceptionIPAddressNotAllowed {
		t.Fatalf("new session: %v", err)
	}
}

func TestQueriesJudgedByIPACL(t *testing.T) {
	query := func(address string) error {
		db := chdriver.OpenDB(&chdriver.Options{Addr: []string{address}})
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var got string
		return db.QueryRowContext(ctx, "SELECT 1").Scan(&got)
	}
	serve := func(o ipacl.Options) string {
		list, _, err := ipacl.Compile(o)
		if err != nil {
			t.Fatal(err)
		}
		h := aclhandler.Middleware(list, "bridge", aclhandler.ScopeBackend, echoJSONHandler())
		return startTestProtocolServer(t, New(h, nil, false, "ip-acl"))
	}

	// each query is judged by the session's address, the loopback here
	if err := query(serve(ipacl.Options{Allow: []string{"127.0.0.1"}})); err != nil {
		t.Fatalf("allowed session: %v", err)
	}
	var exception *chdriver.Exception
	if err := query(serve(ipacl.Options{})); !errors.As(err, &exception) || exception.Code != 62 ||
		!strings.Contains(exception.Message, "403") {
		t.Fatalf("rejected query: %v", err)
	}
	// a drop ends the session with no answer, and the server serves the next one
	address := serve(ipacl.Options{Action: "drop"})
	for range 2 {
		if err := query(address); err == nil || errors.As(err, &exception) {
			t.Fatalf("dropped query: %v", err)
		}
	}
}

func TestServeQueryRepanics(t *testing.T) {
	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("recovered %v", p)
		}
	}()
	serveQuery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), nil, nil)
}
