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
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vtenv"
)

const (
	// Origin behavior is selected by a marker inside the statement text so a
	// single fake origin can serve every lifecycle scenario.
	markerStatementError = "trickster_error"
	markerDropConnection = "trickster_drop"
	markerStall          = "trickster_stall"
	markerInterrupted    = "trickster_interrupt"
	markerPartialStall   = "trickster_partial_stall"
)

// lifecycleOriginHandler fails, drops, or stalls statements on demand and
// records which origin connection served each one.
type lifecycleOriginHandler struct {
	testOriginHandler
	mtx         sync.Mutex
	statements  []lifecycleStatement
	connections int
	release     <-chan struct{}
	respond     func(string) *sqltypes.Result
}

// setResponder overrides the result the origin returns for ordinary queries.
func (h *lifecycleOriginHandler) setResponder(respond func(string) *sqltypes.Result) {
	h.mtx.Lock()
	h.respond = respond
	h.mtx.Unlock()
}

type lifecycleStatement struct {
	connectionID uint32
	query        string
}

func (h *lifecycleOriginHandler) NewConnection(*vtmysql.Conn) {
	h.mtx.Lock()
	h.connections++
	h.mtx.Unlock()
}

func (h *lifecycleOriginHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	h.mtx.Lock()
	h.statements = append(h.statements,
		lifecycleStatement{connectionID: c.ConnectionID, query: query})
	respond := h.respond
	h.mtx.Unlock()
	switch {
	case strings.Contains(query, markerStatementError):
		// An ordinary server error: a complete, synchronized ERR response that
		// leaves the connection perfectly usable.
		return sqlerror.NewSQLError(sqlerror.ERParseError, sqlerror.SSClientError,
			"You have an error in your SQL syntax")
	case strings.Contains(query, markerInterrupted):
		// KILL QUERY: the statement ends, the connection does not.
		return sqlerror.NewSQLError(sqlerror.ERQueryInterrupted, sqlerror.SSQueryInterrupted,
			"Query execution was interrupted")
	case strings.Contains(query, markerDropConnection):
		c.Close()
		return io.EOF
	case strings.Contains(query, markerPartialStall):
		// Send a result set larger than the write buffer, then go quiet
		// without closing, so a drain of the remainder never completes.
		value := strings.Repeat("x", 4096)
		rows := make([][]sqltypes.Value, resultBatchSize*4)
		for i := range rows {
			rows[i] = []sqltypes.Value{sqltypes.NewVarBinary(value)}
		}
		if err := callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_VARBINARY}}, Rows: rows,
		}); err != nil {
			return err
		}
		<-h.release
		return io.EOF
	case strings.Contains(query, markerStall):
		<-h.release
		return io.EOF
	}
	if respond != nil {
		return callback(respond(query))
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		return callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
			Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
		})
	}
	return callback(&sqltypes.Result{RowsAffected: 1})
}

func (h *lifecycleOriginHandler) connectionCount() int {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return h.connections
}

// statementCount reports how many statements matching substr reached the origin.
func (h *lifecycleOriginHandler) statementCount(substr string) int {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	count := 0
	for _, statement := range h.statements {
		if strings.Contains(statement.query, substr) {
			count++
		}
	}
	return count
}

// statementConnections returns the distinct origin connections that served
// statements matching substr, in first-seen order.
func (h *lifecycleOriginHandler) statementConnections(substr string) []uint32 {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	var seen []uint32
	for _, statement := range h.statements {
		if !strings.Contains(statement.query, substr) {
			continue
		}
		if len(seen) == 0 || seen[len(seen)-1] != statement.connectionID {
			seen = append(seen, statement.connectionID)
		}
	}
	return seen
}

// startLifecycleProxy runs a fake origin behind a proxy and returns both, along
// with a connected client.
func startLifecycleProxy(t *testing.T, name string, queryTimeout time.Duration,
	configure ...func(*ProtocolConfig),
) (*lifecycleOriginHandler, *ProtocolServer, *vtmysql.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	handler := &lifecycleOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}, release: release,
	}
	origin, err := vtmysql.NewFromListener(listener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	t.Cleanup(origin.Shutdown)
	originAddress := listener.Addr().(*net.TCPAddr)

	config := ProtocolConfig{
		BackendName: name, ProxyOnly: true,
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddress.Port,
			Uname: "origin", Pass: "origin-password"},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, QueryTimeout: queryTimeout,
	}
	for _, apply := range configure {
		apply(&config)
	}
	server, err := NewProtocolServer(config)
	if err != nil {
		t.Fatal(err)
	}
	port := serveTestProxy(t, server)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: port, Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return handler, server, client
}

func sqlErrorNumber(t *testing.T, err error) sqlerror.ErrorCode {
	t.Helper()
	var sqlErr *sqlerror.SQLError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("error %v is not a MySQL protocol error", err)
	}
	return sqlErr.Number()
}

// TestOrdinarySQLErrorKeepsTransactionOnOneOriginConnection covers the concrete
// sequence from the review: a statement error inside a transaction must not
// silently move the following write onto a new autocommit connection.
func TestOrdinarySQLErrorKeepsTransactionOnOneOriginConnection(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-statement-error", time.Second)

	if _, err := client.ExecuteFetch("BEGIN", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	_, err := client.ExecuteFetch("select "+markerStatementError, vtmysql.FETCH_ALL_ROWS, true)
	if err == nil {
		t.Fatal("the origin's statement error did not reach the client")
	}
	if got := sqlErrorNumber(t, err); got != sqlerror.ERParseError {
		t.Fatalf("statement error number = %d, want %d", got, sqlerror.ERParseError)
	}
	if _, err = client.ExecuteFetch("update metrics set value = 1",
		vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatalf("the transaction was unusable after an ordinary SQL error: %v", err)
	}

	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the statement error discarded a usable connection", got)
	}
	begin := origin.statementConnections("BEGIN")
	update := origin.statementConnections("update metrics")
	if len(begin) != 1 || len(update) != 1 || begin[0] != update[0] {
		t.Fatalf("BEGIN ran on origin connections %v and the UPDATE on %v; want one shared connection",
			begin, update)
	}
}

// TestConnectionLossInTransactionRefusesTransparentReconnect asserts a lost
// upstream inside a transaction closes the downstream session rather than
// reconnecting into autocommit.
func TestConnectionLossInTransactionRefusesTransparentReconnect(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-tx-loss", time.Second)

	if _, err := client.ExecuteFetch("BEGIN", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecuteFetch("select "+markerDropConnection,
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a lost upstream connection did not surface an error")
	}
	if _, err := client.ExecuteFetch("update metrics set value = 1",
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("an UPDATE ran after the transaction's session state was lost")
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the session silently reconnected", got)
	}
}

// TestConnectionLossInStatefulSessionRefusesTransparentReconnect covers session
// state outside a transaction — here a user variable a reconnect cannot replay.
func TestConnectionLossInStatefulSessionRefusesTransparentReconnect(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-stateful-loss", time.Second)

	if _, err := client.ExecuteFetch("SET @report_id = 7", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecuteFetch("select "+markerDropConnection,
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a lost upstream connection did not surface an error")
	}
	if _, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a query ran after the session's user variables were lost")
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the session silently reconnected", got)
	}
}

// TestConnectionLossDuringStatefulStatementRefusesReconnect covers the ordering
// the terminal decision depends on. The session holds no state until the very
// statement that loses the connection, so the state a partially applied SET
// leaves behind must be recorded before the reconnect decision is made.
func TestConnectionLossDuringStatefulStatementRefusesReconnect(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-stateful-loss-inline", time.Second)

	if _, err := client.ExecuteFetch("SET @report = '"+markerDropConnection+"'",
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a lost upstream connection did not surface an error")
	}
	if _, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a query ran after a partially applied SET lost its connection")
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the session silently reconnected", got)
	}
}

// TestConnectionLossAfterWriteRefusesReconnect covers the connection-scoped
// diagnostics a successful write leaves behind. LAST_INSERT_ID would silently
// report a different value on a replacement connection.
func TestConnectionLossAfterWriteRefusesReconnect(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-write-loss", time.Second)

	if _, err := client.ExecuteFetch("insert into metrics (value) values (1)",
		vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecuteFetch("select "+markerDropConnection,
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a lost upstream connection did not surface an error")
	}
	if _, err := client.ExecuteFetch("select last_insert_id()",
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("LAST_INSERT_ID was served from a replacement connection")
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the session silently reconnected", got)
	}
}

// TestInterruptedQueryKeepsTransaction asserts an externally killed query does
// not destroy an otherwise healthy transaction. Vitess classifies
// ER_QUERY_INTERRUPTED as a connection error; the origin connection is fine.
func TestInterruptedQueryKeepsTransaction(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-interrupted", time.Second)

	if _, err := client.ExecuteFetch("BEGIN", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	_, err := client.ExecuteFetch("select "+markerInterrupted, vtmysql.FETCH_ALL_ROWS, true)
	if err == nil {
		t.Fatal("the interrupted query did not surface an error")
	}
	if got := sqlErrorNumber(t, err); got != sqlerror.ERQueryInterrupted {
		t.Fatalf("interrupted error number = %d, want %d", got, sqlerror.ERQueryInterrupted)
	}
	if _, err = client.ExecuteFetch("update metrics set value = 1",
		vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatalf("the transaction was destroyed by an interrupted query: %v", err)
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: an interrupted query discarded a usable connection", got)
	}
}

// TestAbandonedResultStreamIsClosedNotDrained asserts Trickster does not try to
// drain a stream it has already classified as fatal. The origin here goes quiet
// without closing, so a drain would block until a deadline that is not set on
// the upstream connection.
func TestAbandonedResultStreamIsClosedNotDrained(t *testing.T) {
	// No query timeout: a timeout would close the upstream and mask a drain
	// that never completes on its own.
	_, _, client := startLifecycleProxy(t, "mysql-abandoned-stream", 0,
		func(config *ProtocolConfig) { config.MaxResultRows = 2 })

	done := make(chan error, 1)
	go func() {
		_, err := client.ExecuteFetch("select "+markerPartialStall, vtmysql.FETCH_ALL_ROWS, true)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the result-row limit did not fail the query")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy blocked draining a result stream it had already abandoned")
	}
}

// TestConnectionLossInReplayableSessionStillReconnects preserves the existing
// transparent reconnect for sessions holding only replayable state.
func TestConnectionLossInReplayableSessionStillReconnects(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-replayable-loss", time.Second)

	if _, err := client.ExecuteFetch("select "+markerDropConnection,
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("a lost upstream connection did not surface an error")
	}
	result, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatalf("a replayable session did not reconnect: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "42" {
		t.Fatalf("result after reconnect = %+v, want 42", result)
	}
	if got := origin.connectionCount(); got != 2 {
		t.Fatalf("origin connections = %d, want 2", got)
	}
}

// TestQueryTimeoutInTransactionRefusesTransparentReconnect applies the same
// terminal-state rule to a timeout, which also destroys the upstream.
func TestQueryTimeoutInTransactionRefusesTransparentReconnect(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-timeout-tx", 25*time.Millisecond)

	if _, err := client.ExecuteFetch("BEGIN", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecuteFetch("select "+markerStall,
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("the stalled query did not time out")
	}
	if _, err := client.ExecuteFetch("update metrics set value = 1",
		vtmysql.FETCH_ALL_ROWS, true); err == nil {
		t.Fatal("an UPDATE ran after a timeout destroyed the transaction")
	}
	if got := origin.connectionCount(); got != 1 {
		t.Fatalf("origin connections = %d, want 1: the session silently reconnected", got)
	}
}

// TestUpstreamRetainableClassification pins the rule that decides whether an
// origin connection survives a failed statement.
func TestUpstreamRetainableClassification(t *testing.T) {
	statementError := sqlerror.NewSQLError(sqlerror.ERParseError, sqlerror.SSClientError, "syntax")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "no error", want: true},
		{name: "ordinary statement error", err: statementError, want: true},
		{name: "connection lost", want: false, err: sqlerror.NewSQLError(
			sqlerror.CRServerLost, sqlerror.SSUnknownSQLState, "lost")},
		{name: "server gone", want: false, err: sqlerror.NewSQLError(
			sqlerror.CRServerGone, sqlerror.SSUnknownSQLState, "gone")},
		{name: "abandoned stream", err: upstreamFatal{err: statementError}, want: false},
		{name: "non-protocol error", err: io.EOF, want: false},
		// KILL QUERY arrives as a complete ERR packet and leaves the
		// connection synchronized, even though sqlerror.IsConnErr counts it.
		{name: "query interrupted", want: true, err: sqlerror.NewSQLError(
			sqlerror.ERQueryInterrupted, sqlerror.SSQueryInterrupted, "interrupted")},
		// The server-terminal codes sqlerror.IsConnErr omits.
		{name: "server shutdown", want: false, err: sqlerror.NewSQLError(
			sqlerror.ERServerShutdown, sqlerror.SSUnknownSQLState, "shutdown")},
		{name: "forcing close", want: false, err: sqlerror.NewSQLError(
			sqlerror.ERForcingClose, sqlerror.SSUnknownSQLState, "closing")},
		{name: "aborting connection", want: false, err: sqlerror.NewSQLError(
			sqlerror.ERAbortingConnection, sqlerror.SSUnknownSQLState, "aborting")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamRetainable(&vtmysql.Conn{}, tc.err); got != tc.want {
				t.Fatalf("upstreamRetainable(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
	// The fatality marker is internal: the client still receives the origin's
	// own error so Vitess can encode its errno.
	if got := sqlErrorNumber(t, originError(upstreamFatal{err: statementError})); got != sqlerror.ERParseError {
		t.Fatalf("unwrapped error number = %d, want %d", got, sqlerror.ERParseError)
	}
}

// TestSessionStatefulClassification separates state a reconnect can replay from
// state it cannot. Only the database and time zone are replayable.
func TestSessionStatefulClassification(t *testing.T) {
	for _, tc := range []struct {
		query    string
		stateful bool
	}{
		{query: "SELECT count(*) FROM trips"},
		{query: "VALUES ROW(1), ROW(2)"},
		{query: "TABLE trips"},
		{query: "CHECK TABLE trips"},
		{query: "CHECKSUM TABLE trips"},
		{query: "SHOW TABLES"},
		{query: "USE analytics"},
		{query: "SET time_zone = '+00:00'"},
		{query: "UPDATE trips SET fare = 1 WHERE id = 2", stateful: true},
		{query: "DELETE FROM trips WHERE id = 2", stateful: true},
		{query: "INSERT INTO trips (fare) VALUES (1)", stateful: true},
		{query: "SET @report_id = 7", stateful: true},
		{query: "SET SESSION sql_mode = 'ANSI'", stateful: true},
		{query: "SELECT GET_LOCK('report', 1)", stateful: true},
		{query: "SELECT @@sql_mode", stateful: true},
		{query: "CREATE TEMPORARY TABLE staging (id int)", stateful: true},
		{query: "LOCK TABLES trips READ", stateful: true},
		{query: "OPTIMIZE TABLE trips", stateful: true},
		{query: "EXPLAIN FORMAT=JSON INTO @plan SELECT * FROM trips", stateful: true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			h := &protocolHandler{}
			session := &upstreamSession{}
			h.updateSessionStateParsed(session, parseQuery(tc.query))
			if session.stateful != tc.stateful {
				t.Fatalf("stateful = %t, want %t", session.stateful, tc.stateful)
			}
			if session.terminal {
				t.Fatal("a successful statement made the session terminal")
			}
		})
	}
	// A transaction is unreplayable while it is open, and is tracked by inTx
	// rather than by the sticky stateful flag.
	h := &protocolHandler{}
	session := &upstreamSession{}
	h.updateSessionStateParsed(session, parseQuery("BEGIN"))
	if !session.inTx || session.stateful {
		t.Fatalf("BEGIN state = %+v, want an open transaction and no sticky state", session)
	}
}

// TestFailedStatementRecordsPossibleSessionState covers a failed statement that
// can still have applied part of its work before the origin reported an error.
func TestFailedStatementRecordsPossibleSessionState(t *testing.T) {
	for _, tc := range []struct {
		query    string
		stateful bool
	}{
		{query: "SELECT count(*) FROM trips"},
		{query: "SELECT this is not valid sql"},
		{query: "BEGIN"},
		{query: "UPDATE trips SET fare = 1 WHERE id = 2", stateful: true},
		{query: "SET @a = 1, @b = broken", stateful: true},
		{query: "SET time_zone = '+00:00'", stateful: true},
		{query: "CREATE TEMPORARY TABLE staging (id int)", stateful: true},
		{query: "SELECT GET_LOCK('report', 1)", stateful: true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			h := &protocolHandler{}
			session := &upstreamSession{}
			h.updateSessionStateFailed(session, parseQuery(tc.query))
			if session.stateful != tc.stateful {
				t.Fatalf("stateful after failure = %t, want %t", session.stateful, tc.stateful)
			}
			if session.timeZone != "" || session.inTx {
				t.Fatalf("a failed statement applied tracked state: %+v", session)
			}
		})
	}
}

// TestAbandonUpstreamMarksUnreplayableSessionsTerminal isolates the rule shared
// by the timeout and connection-loss paths.
func TestAbandonUpstreamMarksUnreplayableSessionsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*upstreamSession)
		terminal bool
	}{
		{name: "replayable", mutate: func(*upstreamSession) {}},
		{name: "in transaction", terminal: true,
			mutate: func(s *upstreamSession) { s.inTx = true }},
		{name: "stateful", terminal: true,
			mutate: func(s *upstreamSession) { s.stateful = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-abandon"}, nil)
			downstream := &vtmysql.Conn{}
			session := &upstreamSession{downstream: downstream}
			tc.mutate(session)
			h.abandonUpstream(session, nil, "origin_query")
			if session.terminal != tc.terminal {
				t.Fatalf("terminal = %t, want %t", session.terminal, tc.terminal)
			}
			if downstream.IsMarkedForClose() != tc.terminal {
				t.Fatalf("downstream marked for close = %t, want %t",
					downstream.IsMarkedForClose(), tc.terminal)
			}
		})
	}
}

// TestTerminalSessionRefusesReconnect asserts the refusal happens before any
// replacement connection is opened.
func TestTerminalSessionRefusesReconnect(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-terminal"}, nil)
	session := &upstreamSession{upstreamParamsReady: true, terminal: true}
	err := h.connectSession(session)
	if err == nil {
		t.Fatal("a terminal session opened a replacement upstream connection")
	}
	if !strings.Contains(err.Error(), "session state") {
		t.Fatalf("terminal reconnect error = %v", err)
	}
	if session.conn != nil {
		t.Fatal("a terminal session retained an upstream connection")
	}
}

func TestParsedQueryStatementTypesUsedByLifecycleRules(t *testing.T) {
	// Most classifications come from Preview; leading WITH statements require
	// the parsed AST because Preview reports them as unknown.
	for query, want := range map[string]sqlparser.StatementType{
		"SET @report_id = 7":                                  sqlparser.StmtSet,
		"CREATE TEMPORARY TABLE t (id int)":                   sqlparser.StmtDDL,
		"LOCK TABLES trips READ":                              sqlparser.StmtLockTables,
		"UPDATE trips SET fare = 1":                           sqlparser.StmtUpdate,
		"SELECT GET_LOCK('report', 1)":                        sqlparser.StmtSelect,
		"WITH source AS (SELECT 1) SELECT * FROM source":      sqlparser.StmtSelect,
		"WITH source AS (SELECT 1) UPDATE trips SET fare = 1": sqlparser.StmtUpdate,
		"VALUES ROW(1), ROW(2)":                               sqlparser.StmtSelect,
		"BEGIN":                                               sqlparser.StmtBegin,
	} {
		if got := parseQuery(query).statementType; got != want {
			t.Fatalf("%q statement type = %v, want %v", query, got, want)
		}
	}
}

// TestResponseDispatchUsesParsedShape covers statements whose leading keyword
// does not describe their packet shape, plus SELECT ... INTO, whose SELECT
// keyword suggests rows even though MySQL returns an OK packet.
func TestResponseDispatchUsesParsedShape(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-response-shape", time.Second)
	origin.setResponder(func(query string) *sqltypes.Result {
		keyword := strings.ToUpper(strings.TrimSpace(query))
		rowResponse := strings.HasPrefix(keyword, "WITH") ||
			strings.HasPrefix(keyword, "VALUES") || strings.HasPrefix(keyword, "TABLE") ||
			strings.HasPrefix(keyword, "CHECK ") || strings.HasPrefix(keyword, "CHECKSUM ") ||
			strings.HasPrefix(keyword, "OPTIMIZE ") || strings.HasPrefix(keyword, "REPAIR ") ||
			strings.Contains(keyword, " CHECK PARTITION ")
		if rowResponse {
			return &sqltypes.Result{
				Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_INT64}},
				Rows: [][]sqltypes.Value{
					{sqltypes.NewInt64(1)},
					{sqltypes.NewInt64(2)},
				},
			}
		}
		return &sqltypes.Result{RowsAffected: 2, InsertID: 17, InsertIDChanged: true}
	})

	cte := "WITH source AS (SELECT 1 UNION ALL SELECT 2) SELECT * FROM source"
	result, err := client.ExecuteFetch(cte, vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatalf("multi-row CTE failed: %v", err)
	}
	if len(result.Rows) != 2 || result.Rows[0][0].ToString() != "1" ||
		result.Rows[1][0].ToString() != "2" {
		t.Fatalf("multi-row CTE result = %+v, want rows 1 and 2", result.Rows)
	}

	result, err = client.ExecuteFetch("VALUES ROW(1), ROW(2)", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatalf("multi-row VALUES failed: %v", err)
	}
	if len(result.Rows) != 2 || result.Rows[0][0].ToString() != "1" ||
		result.Rows[1][0].ToString() != "2" {
		t.Fatalf("multi-row VALUES result = %+v, want rows 1 and 2", result.Rows)
	}

	for _, query := range []string{
		"TABLE trips",
		"CHECK TABLE trips, fares",
		"CHECKSUM TABLE trips, fares",
		"OPTIMIZE TABLE trips",
		"REPAIR TABLE trips",
		"ALTER TABLE trips CHECK PARTITION p0",
	} {
		result, err = client.ExecuteFetch(query, vtmysql.FETCH_ALL_ROWS, true)
		if err != nil {
			t.Fatalf("%s failed: %v", query, err)
		}
		if len(result.Rows) != 2 {
			t.Fatalf("%s returned %d rows, want 2", query, len(result.Rows))
		}
	}

	result, err = client.ExecuteFetch("SELECT 42 INTO @answer", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatalf("SELECT INTO failed: %v", err)
	}
	if len(result.Fields) != 0 || len(result.Rows) != 0 || result.RowsAffected != 2 ||
		result.InsertID != 17 || !result.InsertIDChanged {
		t.Fatalf("SELECT INTO result = %+v, want OK metadata with 2 affected rows and insert ID 17",
			result)
	}

	result, err = client.ExecuteFetch(
		"EXPLAIN FORMAT=JSON INTO @plan SELECT * FROM trips", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatalf("EXPLAIN INTO failed: %v", err)
	}
	if len(result.Fields) != 0 || len(result.Rows) != 0 || result.RowsAffected != 2 ||
		result.InsertID != 17 || !result.InsertIDChanged {
		t.Fatalf("EXPLAIN INTO result = %+v, want complete OK metadata", result)
	}
}

func TestObscureAdministrativeResponseShapesAreRejected(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-obscure-shapes", time.Second)
	for _, query := range []string{
		"HELP 'SELECT'",
		"XA RECOVER",
		"HANDLER trips READ FIRST LIMIT 2",
		"CACHE INDEX trips IN hot_cache",
		"SELECT 1 /*!80400 INTO @answer */",
		"SELECT /*!80400 @tenant := 1, */ 1",
	} {
		if _, err := client.ExecuteFetch(query, vtmysql.FETCH_ALL_ROWS, true); err == nil {
			t.Fatalf("%s was not rejected", query)
		}
		if got := origin.statementCount(query); got != 0 {
			t.Fatalf("%s reached the origin %d times", query, got)
		}
	}
}
