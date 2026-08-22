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

package mysql

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/collations"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtenv"
)

const (
	latin1SwedishCI  = collations.ID(8)
	utf8mb40900AICI  = collations.ID(255)
	utf8mb4GeneralCI = collations.ID(45)
)

// handshakeOriginHandler records the statements an origin received and the
// collation each upstream connection negotiated during its handshake.
type handshakeOriginHandler struct {
	testOriginHandler
	mtx        sync.Mutex
	queries    []string
	handshakes []collations.ID
}

func (h *handshakeOriginHandler) ConnectionReady(c *vtmysql.Conn) {
	h.mtx.Lock()
	h.handshakes = append(h.handshakes, c.CharacterSet)
	h.mtx.Unlock()
}

func (h *handshakeOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	h.mtx.Lock()
	h.queries = append(h.queries, query)
	h.mtx.Unlock()
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		return callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
			Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
		})
	}
	return callback(&sqltypes.Result{})
}

func (h *handshakeOriginHandler) recordedQueries() []string {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return append([]string(nil), h.queries...)
}

func (h *handshakeOriginHandler) negotiatedCollations() []collations.ID {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return append([]collations.ID(nil), h.handshakes...)
}

func startHandshakeOrigin(t *testing.T) (*handshakeOriginHandler, vtmysql.ConnParams) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &handshakeOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()},
	}
	origin, err := vtmysql.NewFromListener(listener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	t.Cleanup(origin.Shutdown)
	address := listener.Addr().(*net.TCPAddr)
	return handler, vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port, Uname: "origin", Pass: "origin-password",
	}
}

// serveTestProxy runs server on a fresh loopback listener and returns its port.
func serveTestProxy(t *testing.T, server *ProtocolServer) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-serveDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("protocol server did not stop")
		}
	})
	return listener.Addr().(*net.TCPAddr).Port
}

func newRouteBackend(t *testing.T, name string) backends.Backend {
	t.Helper()
	opts := bo.New()
	opts.Name = name
	opts.Provider = providers.MySQL
	opts.OriginURL = "http://example.com"
	backend, err := backends.New(name, opts, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func newRoutedTestServer(t *testing.T, upstream vtmysql.ConnParams) *ProtocolServer {
	t.Helper()
	server, err := NewRoutedProtocolServer(ProtocolConfig{
		BackendName: "mysql-handshake-users", ConnectTimeout: time.Second,
		DownstreamUsers: map[string]string{"alice": "alice-password"},
	}, testRouteResolver{"alice": {Backend: newRouteBackend(t, "mysql-handshake")}},
		map[string]ProtocolConfig{
			"mysql-handshake": {
				BackendName: "mysql-handshake", Upstream: upstream,
				DownstreamUsers: map[string]string{"alice": "alice-password"},
				ConnectTimeout:  time.Second,
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// TestRoutedHandshakeActivatesTargetForInitialDatabase covers the routed DSN
// that names a database. Vitess dispatches the resulting USE before it reports
// the connection as ready, so the handshake fails unless that command can
// activate the route itself.
func TestRoutedHandshakeActivatesTargetForInitialDatabase(t *testing.T) {
	origin, upstream := startHandshakeOrigin(t)
	server := newRoutedTestServer(t, upstream)
	port := serveTestProxy(t, server)

	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: port, Uname: "alice", Pass: "alice-password",
		DbName: "reporting",
	})
	if err != nil {
		t.Fatalf("routed handshake with an initial database failed: %v", err)
	}
	defer client.Close()

	queries := origin.recordedQueries()
	if len(queries) != 1 || !strings.EqualFold(queries[0], "use `reporting`") {
		t.Fatalf("origin statements after the handshake = %v, want the initial USE", queries)
	}
	result, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "42" {
		t.Fatalf("routed result = %+v, want 42", result)
	}

	target := server.routedHandler.targets["mysql-handshake"]
	server.routedHandler.mtx.Lock()
	pending := len(server.routedHandler.controls)
	server.routedHandler.mtx.Unlock()
	if pending != 0 {
		t.Fatalf("routed handler retained %d pending connection controls", pending)
	}
	target.mtx.Lock()
	transferred := len(target.controls)
	sessions := len(target.sessions)
	var session *upstreamSession
	for _, s := range target.sessions {
		session = s
	}
	target.mtx.Unlock()
	if transferred != 1 {
		t.Fatalf("target connection controls = %d, want 1", transferred)
	}
	if sessions != 1 {
		t.Fatalf("target sessions = %d, want 1", sessions)
	}
	session.mtx.Lock()
	ready, database := session.ready, session.database
	session.mtx.Unlock()
	if !ready {
		t.Fatal("target never observed ConnectionReady for the routed connection")
	}
	if database != "reporting" {
		t.Fatalf("session database = %q, want reporting", database)
	}
}

// TestRoutedHandshakeActivatesWithoutInitialDatabase keeps the ConnectionReady
// activation path working for clients that omit CLIENT_CONNECT_WITH_DB.
func TestRoutedHandshakeActivatesWithoutInitialDatabase(t *testing.T) {
	origin, upstream := startHandshakeOrigin(t)
	server := newRoutedTestServer(t, upstream)
	port := serveTestProxy(t, server)

	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: port, Uname: "alice", Pass: "alice-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "42" {
		t.Fatalf("routed result = %+v, want 42", result)
	}
	for _, query := range origin.recordedQueries() {
		if strings.HasPrefix(strings.ToLower(query), "use ") {
			t.Fatalf("origin received an unexpected initial database statement %q", query)
		}
	}
}

func newRoutedHandlerForTest(t *testing.T) (*routedProtocolHandler, *protocolHandler) {
	t.Helper()
	env, err := vtenv.New(vtenv.Options{MySQLServerVersion: protocolVersion})
	if err != nil {
		t.Fatal(err)
	}
	target := newProtocolHandler(ProtocolConfig{BackendName: "mysql-a"}, env)
	return &routedProtocolHandler{
		env: env, targets: map[string]*protocolHandler{"mysql-a": target},
		controls: make(map[uint32]*phaseConn),
	}, target
}

func newTestControl(t *testing.T) *phaseConn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return newPhaseConn(server, 0, 0, 0, 0)
}

// TestRoutedActivationInitializesTargetExactlyOnce asserts that the lazy
// activation performed by the handshake's USE is not repeated by the later
// ConnectionReady callback, which would discard the session it created.
func TestRoutedActivationInitializesTargetExactlyOnce(t *testing.T) {
	routed, target := newRoutedHandlerForTest(t)
	c := &vtmysql.Conn{ConnectionID: 7}
	control := newTestControl(t)
	routed.setControl(c.ConnectionID, control)
	routed.NewConnection(c)
	if c.StatusFlags != vtmysql.ServerStatusAutocommit {
		t.Fatalf("initial downstream status = %#x, want autocommit", c.StatusFlags)
	}
	c.ClientData = backends.RouteDecision{
		Target:  backends.RouteTarget{Backend: newRouteBackend(t, "mysql-a")},
		Outcome: backends.RouteOutcomeSelected,
	}

	first, err := routed.activate(c)
	if err != nil {
		t.Fatal(err)
	}
	if first != target {
		t.Fatal("activation selected an unexpected target")
	}
	routed.mtx.Lock()
	pending := len(routed.controls)
	routed.mtx.Unlock()
	if pending != 0 {
		t.Fatal("activation left the connection control on the routed handler")
	}
	target.mtx.Lock()
	session := target.sessions[c]
	target.mtx.Unlock()
	if session == nil {
		t.Fatal("activation did not initialize a target session")
	}
	if session.control != control {
		t.Fatal("activation did not transfer the pending connection control")
	}

	second, err := routed.activate(c)
	if err != nil {
		t.Fatal(err)
	}
	if second != target {
		t.Fatal("repeat activation selected an unexpected target")
	}
	target.mtx.Lock()
	repeat := target.sessions[c]
	count := len(target.sessions)
	target.mtx.Unlock()
	if repeat != session {
		t.Fatal("repeat activation replaced the session created by the first")
	}
	if count != 1 {
		t.Fatalf("target sessions = %d, want 1", count)
	}

	routed.ConnectionReady(c)
	target.mtx.Lock()
	afterReady := target.sessions[c]
	target.mtx.Unlock()
	if afterReady != session {
		t.Fatal("ConnectionReady re-initialized an already-activated connection")
	}
	session.mtx.Lock()
	ready := session.ready
	session.mtx.Unlock()
	if !ready {
		t.Fatal("ConnectionReady did not complete the activated session")
	}
	if c.IsMarkedForClose() {
		t.Fatal("a successfully activated connection was marked for close")
	}

	routed.ConnectionClosed(c)
	target.mtx.Lock()
	remaining := len(target.sessions)
	target.mtx.Unlock()
	if remaining != 0 {
		t.Fatalf("target sessions after close = %d, want 0", remaining)
	}
}

// TestRoutedActivationFailureIsTerminal asserts that a connection whose route
// cannot be resolved is closed and can never select a second target.
func TestRoutedActivationFailureIsTerminal(t *testing.T) {
	routed, target := newRoutedHandlerForTest(t)
	c := &vtmysql.Conn{ConnectionID: 9}
	routed.setControl(c.ConnectionID, newTestControl(t))
	routed.NewConnection(c)
	c.ClientData = backends.RouteDecision{
		Target:  backends.RouteTarget{Backend: newRouteBackend(t, "mysql-missing")},
		Outcome: backends.RouteOutcomeSelected,
	}

	if _, err := routed.activate(c); err == nil {
		t.Fatal("activation succeeded for an unknown target")
	}
	if !c.IsMarkedForClose() {
		t.Fatal("a failed activation left the connection open")
	}
	routed.mtx.Lock()
	pending := len(routed.controls)
	routed.mtx.Unlock()
	if pending != 0 {
		t.Fatal("a failed activation retained the pending connection control")
	}
	if _, ok := c.ClientData.(backends.RouteDecision); ok {
		t.Fatal("a failed activation left its route decision available for reuse")
	}
	if _, err := routed.activate(c); err == nil {
		t.Fatal("a second activation succeeded after a terminal failure")
	}
	if _, err := routed.target(c); err == nil {
		t.Fatal("a failed activation still resolves a target")
	}

	routed.ConnectionReady(c)
	routed.ConnectionClosed(c)
	target.mtx.Lock()
	sessions := len(target.sessions)
	target.mtx.Unlock()
	if sessions != 0 {
		t.Fatalf("target sessions after a failed activation = %d, want 0", sessions)
	}
}

// TestHandshakeCollationScopesCacheIdentity asserts that two clients issuing
// identical query bytes under different handshake collations cannot share one
// cache entry.
func TestHandshakeCollationScopesCacheIdentity(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{BackendName: "mysql-collation"}}
	c := &vtmysql.Conn{User: "alice"}
	key := func(id collations.ID) string {
		return h.queryCacheKey(c, &upstreamSession{database: "analytics", collation: id},
			cacheModeOPC, "SELECT name FROM users ORDER BY name")
	}
	latin1, utf8mb4, unset := key(latin1SwedishCI), key(utf8mb40900AICI), key(collations.Unknown)
	if latin1 == utf8mb4 {
		t.Fatal("latin1 and utf8mb4 handshakes share one cache key")
	}
	if latin1 == unset || utf8mb4 == unset {
		t.Fatal("a negotiated handshake collation shares the unnegotiated cache key")
	}
}

// TestHandshakeCollationPropagatesToSessionUpstream covers the per-session
// upstream parameters built from the downstream handshake.
func TestHandshakeCollationPropagatesToSessionUpstream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		downstream collations.ID
		want       collations.ID
	}{
		{name: "latin1", downstream: latin1SwedishCI, want: latin1SwedishCI},
		{name: "utf8mb4", downstream: utf8mb40900AICI, want: utf8mb40900AICI},
		// Collation 0 asks the origin for its default, which leaves the
		// configured upstream collation in place. The session must record that
		// effective collation rather than the zero the client sent.
		{name: "server default", downstream: collations.Unknown, want: utf8mb4GeneralCI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &protocolHandler{config: ProtocolConfig{
				Upstream: vtmysql.ConnParams{Charset: utf8mb4GeneralCI},
			}}
			session := &upstreamSession{upstream: h.config.Upstream, upstreamParamsReady: true}
			downstream := &vtmysql.Conn{
				CharacterSet: tc.downstream, Capabilities: vtmysql.CapabilityClientFoundRows,
			}
			session.mtx.Lock()
			h.applyDownstreamHandshakeLocked(session, downstream)
			session.mtx.Unlock()
			if session.upstream.Charset != tc.want {
				t.Fatalf("upstream charset = %d, want %d", session.upstream.Charset, tc.want)
			}
			if session.collation != tc.want {
				t.Fatalf("session collation = %d, want %d", session.collation, tc.want)
			}
			if session.upstream.Flags&uint64(vtmysql.CapabilityClientFoundRows) == 0 {
				t.Fatal("CLIENT_FOUND_ROWS propagation regressed")
			}
		})
	}
}

// TestHandshakeCollationReachesOriginAcrossReconnect asserts the downstream
// handshake collation is what the origin negotiates, and that it survives a
// transparent upstream reconnect.
func TestHandshakeCollationReachesOriginAcrossReconnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   collations.ID
	}{
		{name: "latin1", id: latin1SwedishCI},
		{name: "utf8mb4", id: utf8mb40900AICI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, upstream := startHandshakeOrigin(t)
			server, err := NewProtocolServer(ProtocolConfig{
				BackendName: "mysql-collation-" + tc.name, Upstream: upstream,
				DownstreamUsers: map[string]string{"alice": "alice-password"},
				ConnectTimeout:  time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			port := serveTestProxy(t, server)
			client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
				Host: "127.0.0.1", Port: port, Uname: "alice", Pass: "alice-password",
				Charset: tc.id,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
				t.Fatal(err)
			}
			if got := origin.negotiatedCollations(); len(got) != 1 || got[0] != tc.id {
				t.Fatalf("origin handshake collations = %v, want [%d]", got, tc.id)
			}

			server.handler.mtx.Lock()
			var session *upstreamSession
			for _, s := range server.handler.sessions {
				session = s
			}
			server.handler.mtx.Unlock()
			session.mtx.Lock()
			originConn := session.conn
			session.mtx.Unlock()
			server.handler.discardUpstream(session, originConn)

			if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
				t.Fatal(err)
			}
			got := origin.negotiatedCollations()
			if len(got) != 2 || got[1] != tc.id {
				t.Fatalf("origin handshake collations after reconnect = %v, want two %d", got, tc.id)
			}
		})
	}
}

// TestComResetConnectionRestoresHandshakeCollation asserts a reset session is
// rebuilt from the connection's original handshake rather than the configured
// upstream default.
func TestComResetConnectionRestoresHandshakeCollation(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{
		BackendName: "mysql-reset", Upstream: vtmysql.ConnParams{Charset: utf8mb4GeneralCI},
	}, nil)
	c := &vtmysql.Conn{ConnectionID: 3, CharacterSet: latin1SwedishCI}
	h.NewConnection(c)
	h.ConnectionReady(c)
	h.ComResetConnection(c)

	h.mtx.Lock()
	session := h.sessions[c]
	h.mtx.Unlock()
	if session == nil {
		t.Fatal("COM_RESET_CONNECTION did not rebuild the session")
	}
	session.mtx.Lock()
	collation, charset := session.collation, session.upstream.Charset
	session.mtx.Unlock()
	if collation != latin1SwedishCI {
		t.Fatalf("reset session collation = %d, want %d", collation, latin1SwedishCI)
	}
	if charset != latin1SwedishCI {
		t.Fatalf("reset upstream charset = %d, want %d", charset, latin1SwedishCI)
	}
	h.ConnectionClosed(c)
}
