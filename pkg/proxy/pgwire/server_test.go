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

package pgwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/crypto/bcrypt"
)

const (
	testBackend       = "pgtest"
	testProvider      = "postgres"
	testClientUser    = "grafana_ro"
	testClientPass    = "client-secret"
	testUpstreamUser  = "trickster"
	testUpstreamPass  = "upstream-secret"
	testDatabase      = "metrics"
	testAppName       = "pgwire-test"
	testProtocolLast  = "max_protocol_version=latest"
	testSSLRequire    = "sslmode=require"
	testSSLDisable    = "sslmode=disable"
	testParamAppName  = "application_name"
	testWaitStep      = 5 * time.Millisecond
	testShortTimeout  = 150 * time.Millisecond
	testUnusedAddress = "127.0.0.1:1"
)

func testConfig(f *fakeUpstream) Config {
	c := Config{
		BackendName: testBackend, Provider: testProvider, RestartKey: "restart-key",
		Upstream:       Upstream{Address: f.address(), Host: fakeTestCertName},
		ConnectTimeout: fakeTimeout,
	}
	c.ApplyListenerOptions(nil)
	c.HandshakeTimeout = fakeTimeout
	return c
}

func terminatedConfig(f *fakeUpstream, stored string) Config {
	c := testConfig(f)
	c.Users = map[string]string{testClientUser: stored}
	c.Upstream.User, c.Upstream.Password, c.Upstream.Database = testUpstreamUser, testUpstreamPass, testDatabase
	return c
}

func cleartextUpstream(f *fakeUpstream) {
	f.authMode, f.user, f.password = fakeAuthCleartext, testUpstreamUser, testUpstreamPass
}

func startServer(t *testing.T, config Config) (*Server, string) {
	t.Helper()
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	return server, serveTestServer(t, server)
}

func serveTestServer(t *testing.T, server *Server) string {
	t.Helper()
	l, err := net.Listen("tcp", fakeLoopbackListen)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return l.Addr().String()
}

func dial(t *testing.T, address, user, password string, settings ...string) (*pgconn.PgConn, error) {
	t.Helper()
	if !strings.Contains(strings.Join(settings, "&"), "sslmode=") {
		settings = append(settings, testSSLDisable)
	}
	config, err := pgconn.ParseConfig(fmt.Sprintf("postgres://%s:%s@%s/%s?%s",
		user, password, address, testDatabase, strings.Join(settings, "&")))
	if err != nil {
		t.Fatal(err)
	}
	if config.TLSConfig != nil {
		config.TLSConfig.InsecureSkipVerify = true
	}
	for _, fallback := range config.Fallbacks {
		if fallback.TLSConfig != nil {
			fallback.TLSConfig.InsecureSkipVerify = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	return pgconn.ConnectConfig(ctx, config)
}

func mustDial(t *testing.T, address, user, password string, settings ...string) *pgconn.PgConn {
	t.Helper()
	conn, err := dial(t, address, user, password, settings...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func queryRows(t *testing.T, conn *pgconn.PgConn, sql string) (int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	results, err := conn.Exec(ctx, sql).ReadAll()
	rows := 0
	for _, result := range results {
		rows += len(result.Rows)
	}
	return rows, err
}

func sqlstate(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(fakeTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(testWaitStep)
	}
}

func TestPassthroughRelaysEveryAuthMethod(t *testing.T) {
	for _, mode := range []string{fakeAuthTrust, fakeAuthCleartext, fakeAuthMD5, fakeAuthSCRAM} {
		t.Run(mode, func(t *testing.T) {
			upstream := newFakeUpstream(t, func(f *fakeUpstream) {
				f.authMode, f.user, f.password = mode, testClientUser, testClientPass
			})
			_, address := startServer(t, testConfig(upstream))
			conn := mustDial(t, address, testClientUser, testClientPass, testParamAppName+"="+testAppName)
			if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
				t.Fatalf("simple query: %d rows, %v", rows, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
			defer cancel()
			result := conn.ExecParams(ctx, "select $1::int", [][]byte{[]byte("1")}, nil, nil, nil).Read()
			if result.Err != nil || len(result.Rows) != 1 {
				t.Fatalf("extended query: %d rows, %v", len(result.Rows), result.Err)
			}
			startup := upstream.lastStartup()
			if startup[paramUser] != testClientUser || startup[paramDatabase] != testDatabase ||
				startup[testParamAppName] != testAppName {
				t.Fatalf("the startup packet was not relayed verbatim: %v", startup)
			}
			if conn.ParameterStatus(fakeParamVersion) != fakeServerVersion {
				t.Fatal("ParameterStatus was not relayed")
			}
			if conn.PID() >= 1000 || bytes.Equal(conn.SecretKey(), []byte{1, 2, 3, 4}) {
				t.Fatalf("the origin's cancellation key leaked: pid %d", conn.PID())
			}
			if mode != fakeAuthTrust {
				if _, err := dial(t, address, testClientUser, "wrong"); sqlstate(err) != sqlstateInvalidPassword {
					t.Fatalf("expected the origin's rejection to be relayed, got %v", err)
				}
			}
		})
	}
}

func TestTerminatedAuthenticatesEveryCredentialFormat(t *testing.T) {
	verifier, err := cred.NewSCRAMVerifier(testClientPass, []byte("0123456789abcdef"), cred.DefaultSCRAMIterations)
	if err != nil {
		t.Fatal(err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(testClientPass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		stored string
		mutate func(*Config)
	}{
		"plaintext":      {stored: testClientPass},
		"scram verifier": {stored: verifier.String()},
		"md5 method":     {stored: cred.PostgresMD5(testClientUser, testClientPass), mutate: func(c *Config) { c.AllowMD5 = true }},
		"md5 cleartext": {stored: cred.PostgresMD5(testClientUser, testClientPass),
			mutate: func(c *Config) { c.AllowCleartextWithoutTLS = true }},
		"bcrypt": {stored: string(hashed), mutate: func(c *Config) { c.AllowCleartextWithoutTLS = true }},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := newFakeUpstream(t, cleartextUpstream)
			config := terminatedConfig(upstream, test.stored)
			if test.mutate != nil {
				test.mutate(&config)
			}
			_, address := startServer(t, config)
			conn := mustDial(t, address, testClientUser, testClientPass, testParamAppName+"="+testAppName)
			if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
				t.Fatalf("query: %d rows, %v", rows, err)
			}
			startup := upstream.lastStartup()
			if startup[paramUser] != testUpstreamUser || startup[paramDatabase] != testDatabase ||
				startup[testParamAppName] != testAppName {
				t.Fatalf("unexpected origin session: %v", startup)
			}
			if conn.ParameterStatus(fakeParamVersion) != fakeServerVersion {
				t.Fatal("the origin's ParameterStatus was not replayed")
			}
			for _, bad := range [][2]string{{testClientUser, "wrong"}, {"nobody", testClientPass}} {
				if _, err := dial(t, address, bad[0], bad[1]); sqlstate(err) != sqlstateInvalidPassword {
					t.Fatalf("%v: expected an authentication failure, got %v", bad, err)
				}
			}
		})
	}
}

func TestTerminatedRefusesMethodsThatNeedTLS(t *testing.T) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(testClientPass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string]string{
		"bcrypt": string(hashed), "md5": cred.PostgresMD5(testClientUser, testClientPass),
	} {
		upstream := newFakeUpstream(t, cleartextUpstream)
		_, address := startServer(t, terminatedConfig(upstream, stored))
		if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateInvalidPassword {
			t.Fatalf("%s: a cleartext password must not be requested without TLS, got %v", name, err)
		}
		if upstream.lastStartup() != nil {
			t.Fatalf("%s: the origin must not be contacted before authentication", name)
		}
	}
}

func TestInboundTLSUsesChannelBinding(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	config := terminatedConfig(upstream, testClientPass)
	config.InboundTLS = testServerTLS(t)
	config.RequireSecureTransport = true
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass, testSSLRequire)
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("query over TLS: %d rows, %v", rows, err)
	}
	if _, ok := conn.Conn().(*tls.Conn); !ok {
		t.Fatal("expected a TLS client connection")
	}
	if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateInvalidAuthSpec {
		t.Fatalf("expected a plaintext client to be refused, got %v", err)
	}
	// a hash credential may use a cleartext password once the channel is encrypted
	hashed, err := bcrypt.GenerateFromPassword([]byte(testClientPass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hashedConfig := terminatedConfig(upstream, string(hashed))
	hashedConfig.InboundTLS = testServerTLS(t)
	_, hashedAddress := startServer(t, hashedConfig)
	mustDial(t, hashedAddress, testClientUser, testClientPass, testSSLRequire)
}

func TestSSLRequestRefusedWithoutCertificate(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := testConfig(upstream)
	config.InboundTLS = &tls.Config{}
	_, address := startServer(t, config)
	if _, err := dial(t, address, testClientUser, testClientPass, testSSLRequire); err == nil {
		t.Fatal("expected sslmode=require to fail against a listener with no certificate")
	}
	conn := mustDial(t, address, testClientUser, testClientPass, "sslmode=prefer")
	if _, ok := conn.Conn().(*tls.Conn); ok {
		t.Fatal("expected the client to fall back to plaintext")
	}
}

func TestUpstreamTLS(t *testing.T) {
	secured := newFakeUpstream(t, func(f *fakeUpstream) {
		cleartextUpstream(f)
		f.tls = testServerTLS(t)
	})
	for name, config := range map[string]Config{
		"passthrough": testConfig(secured), "terminated": terminatedConfig(secured, testClientPass),
	} {
		config.Upstream.TLS = &tls.Config{InsecureSkipVerify: true}
		_, address := startServer(t, config)
		user, password := testClientUser, testClientPass
		if !config.Terminated() {
			user, password = testUpstreamUser, testUpstreamPass
		}
		conn := mustDial(t, address, user, password)
		if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
			t.Fatalf("%s: %d rows, %v", name, rows, err)
		}
	}
	plain := newFakeUpstream(t, nil)
	config := testConfig(plain)
	config.Upstream.TLS = &tls.Config{InsecureSkipVerify: true}
	_, address := startServer(t, config)
	if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateConnectFailure {
		t.Fatalf("expected a connection failure when the origin refuses TLS, got %v", err)
	}
}

func TestProtocol32(t *testing.T) {
	t.Run("passthrough relays a long cancellation key", func(t *testing.T) {
		upstream := newFakeUpstream(t, func(f *fakeUpstream) { f.longSecret = true })
		_, address := startServer(t, testConfig(upstream))
		conn := mustDial(t, address, testClientUser, testClientPass, testProtocolLast)
		if len(conn.SecretKey()) != fakeLongSecretLen {
			t.Fatalf("expected a %d-byte key, got %d", fakeLongSecretLen, len(conn.SecretKey()))
		}
		cancelRunningQuery(t, conn, upstream, fakeLongSecretLen)
	})
	t.Run("terminated negotiates down to 3.0", func(t *testing.T) {
		upstream := newFakeUpstream(t, cleartextUpstream)
		_, address := startServer(t, terminatedConfig(upstream, testClientPass))
		conn := mustDial(t, address, testClientUser, testClientPass, testProtocolLast)
		if len(conn.SecretKey()) != legacySecretLen {
			t.Fatalf("expected a %d-byte key, got %d", legacySecretLen, len(conn.SecretKey()))
		}
	})
}

func cancelRunningQuery(t *testing.T, conn *pgconn.PgConn, upstream *fakeUpstream, secretLen int) {
	// cancels a slow query through the proxy and asserts the
	// origin received its own key, not the one the client holds.
	t.Helper()
	failed := make(chan error, 1)
	go func() {
		_, err := queryRows(t, conn, fakeQuerySlow)
		failed <- err
	}()
	waitFor(t, "the slow query to start", upstream.isRunning)
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	if err := conn.CancelRequest(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-failed; sqlstate(err) != sqlstateCanceled {
		t.Fatalf("expected the query to be canceled, got %v", err)
	}
	forwarded := <-upstream.cancels
	if forwarded.pid < 1000 || len(forwarded.secret) != secretLen || bytes.Equal(forwarded.secret, conn.SecretKey()) {
		t.Fatalf("the origin did not receive its own key: %+v", forwarded)
	}
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("the session must survive a cancel: %d rows, %v", rows, err)
	}
}

func TestCancelRequest(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	for name, config := range map[string]Config{
		"passthrough": testConfig(upstream), "terminated": terminatedConfig(upstream, testClientPass),
	} {
		t.Run(name, func(t *testing.T) {
			_, address := startServer(t, config)
			user, password := testClientUser, testClientPass
			if !config.Terminated() {
				user, password = testUpstreamUser, testUpstreamPass
			}
			cancelRunningQuery(t, mustDial(t, address, user, password), upstream, legacySecretLen)
		})
	}
}

func TestCancelRequestWithUnknownKeyIsDropped(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, testConfig(upstream))
	before := testutil.ToFloat64(metrics.PGWireConnectionErrors.WithLabelValues(testBackend, classCancel))
	for _, packet := range [][]byte{
		{0, 0, 0, 16, 4, 210, 22, 46, 0, 0, 0, 9, 9, 9, 9, 9},
		{0, 0, 0, 10, 4, 210, 22, 46, 0, 0},
	} {
		raw := rawConn(t, address)
		if _, err := raw.Write(packet); err != nil {
			t.Fatal(err)
		}
		expectClosed(t, raw)
	}
	waitFor(t, "the bad key to be counted", func() bool {
		return testutil.ToFloat64(metrics.PGWireConnectionErrors.WithLabelValues(testBackend, classCancel)) > before
	})
	select {
	case forwarded := <-upstream.cancels:
		t.Fatalf("an unknown key must not reach the origin: %+v", forwarded)
	default:
	}
}

func TestClientDisconnectCancelsRunningQuery(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, testConfig(upstream))
	conn, err := dial(t, address, testClientUser, testClientPass)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = queryRows(t, conn, fakeQuerySlow) }()
	waitFor(t, "the slow query to start", upstream.isRunning)
	_ = conn.Conn().Close()
	select {
	case forwarded := <-upstream.cancels:
		if forwarded.pid < 1000 {
			t.Fatalf("unexpected cancel target %+v", forwarded)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("the abandoned query was never canceled at the origin")
	}
}

func TestRelayReportsRequestMetrics(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	server, address := startServer(t, testConfig(upstream))
	conn := mustDial(t, address, testClientUser, testClientPass)
	proxied := testutil.ToFloat64(server.proxied.requests)
	failed := testutil.ToFloat64(server.failed.requests)
	elements := testutil.ToFloat64(server.proxied.elements)
	if rows, err := queryRows(t, conn, fakeQueryMany); err != nil || rows != fakeManyRows {
		t.Fatalf("large result: %d rows, %v", rows, err)
	}
	if _, err := queryRows(t, conn, fakeQueryError); sqlstate(err) != sqlstateDivByZero {
		t.Fatalf("expected the origin's error to be relayed, got %v", err)
	}
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("the session must survive a query error: %d rows, %v", rows, err)
	}
	// a driver's pool keepalive runs no statement and is not a request
	for range 2 {
		if rows, err := queryRows(t, conn, fakeQueryPing); err != nil || rows != 0 {
			t.Fatalf("keepalive: %d rows, %v", rows, err)
		}
	}
	if got := testutil.ToFloat64(server.proxied.requests) - proxied; got != 2 {
		t.Fatalf("expected 2 proxied requests, got %v", got)
	}
	if got := testutil.ToFloat64(server.failed.requests) - failed; got != 1 {
		t.Fatalf("expected 1 failed request, got %v", got)
	}
	if got := testutil.ToFloat64(server.proxied.elements) - elements; got != fakeManyRows+1 {
		t.Fatalf("expected %d elements, got %v", fakeManyRows+1, got)
	}
}

func TestLimits(t *testing.T) {
	t.Run("max upstream connections", func(t *testing.T) {
		upstream := newFakeUpstream(t, nil)
		config := testConfig(upstream)
		config.MaxUpstreamConnections = 1
		_, address := startServer(t, config)
		mustDial(t, address, testClientUser, testClientPass)
		if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateTooManyConns {
			t.Fatalf("expected the second session to be refused, got %v", err)
		}
	})
	t.Run("max message size", func(t *testing.T) {
		upstream := newFakeUpstream(t, nil)
		config := testConfig(upstream)
		config.MaxMessageSizeBytes = 64
		_, address := startServer(t, config)
		conn := mustDial(t, address, testClientUser, testClientPass)
		if _, err := queryRows(t, conn, "select '"+strings.Repeat("x", 256)+"'"); err == nil {
			t.Fatal("expected an oversized message to end the session")
		}
	})
	t.Run("idle timeout", func(t *testing.T) {
		upstream := newFakeUpstream(t, nil)
		config := testConfig(upstream)
		config.IdleTimeout = testShortTimeout
		_, address := startServer(t, config)
		conn := mustDial(t, address, testClientUser, testClientPass)
		if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
			t.Fatalf("an active session must not be closed: %d rows, %v", rows, err)
		}
		time.Sleep(3 * testShortTimeout)
		if _, err := queryRows(t, conn, fakeQueryOne); err == nil {
			t.Fatal("expected the idle session to be closed")
		}
	})
	t.Run("handshake timeout", func(t *testing.T) {
		upstream := newFakeUpstream(t, nil)
		config := testConfig(upstream)
		config.HandshakeTimeout = testShortTimeout
		_, address := startServer(t, config)
		expectClosed(t, rawConn(t, address))
	})
}

func TestOriginUnavailable(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	for name, config := range map[string]Config{
		"passthrough": testConfig(upstream), "terminated": terminatedConfig(upstream, testClientPass),
	} {
		config.Upstream.Address = testUnusedAddress
		config.ConnectTimeout = testShortTimeout
		_, address := startServer(t, config)
		if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateConnectFailure {
			t.Fatalf("%s: expected a connection failure, got %v", name, err)
		}
	}
	// the origin rejects the backend's own credentials
	rejecting := newFakeUpstream(t, func(f *fakeUpstream) {
		f.authMode, f.user, f.password = fakeAuthCleartext, testUpstreamUser, "rotated"
	})
	_, address := startServer(t, terminatedConfig(rejecting, testClientPass))
	if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateConnectFailure {
		t.Fatalf("expected a connection failure, got %v", err)
	}
}

func TestServerLifecycle(t *testing.T) {
	if _, err := NewServer(Config{}); err == nil {
		t.Fatal("expected a server with no origin to be rejected")
	}
	if _, err := NewServer(Config{
		Upstream: Upstream{Address: testUnusedAddress}, Users: map[string]string{testClientUser: cred.SCRAMSHA256Prefix},
	}); err == nil {
		t.Fatal("expected a malformed SCRAM verifier to be rejected")
	}
	upstream := newFakeUpstream(t, nil)
	config := testConfig(upstream)
	config.RequireSecureTransport = true
	insecure, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = insecure.Serve(nil); err == nil {
		t.Fatal("expected secure transport without a certificate to be rejected")
	}
	insecure.UpdateTLSConfig(nil)
	insecure.UpdateTLSConfig(testServerTLS(t))
	if insecure.inboundTLS.Load() == nil || insecure.ProtocolRestartKey() != config.RestartKey {
		t.Fatal("expected the rotated certificate and the restart key to be retained")
	}

	server, address := startServer(t, testConfig(upstream))
	conn := mustDial(t, address, testClientUser, testClientPass)
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	if err = server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = queryRows(t, conn, fakeQueryOne); err == nil {
		t.Fatal("expected shutdown to close the session")
	}
	if server.track(&session{}) {
		t.Fatal("a closing server must not admit sessions")
	}
	expired, expire := context.WithCancel(context.Background())
	expire()
	server.wg.Add(1)
	if err = server.Shutdown(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the shutdown deadline to be reported, got %v", err)
	}
	server.wg.Done()
}

func TestPgOptionsDefaultsAreApplied(t *testing.T) {
	c := Config{}
	c.ApplyListenerOptions(nil)
	if c.IdleTimeout != pgo.DefaultIdleTimeout || c.MaxMessageSizeBytes != pgo.DefaultMaxMessageSizeBytes {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	custom := pgo.NewListener()
	custom.AllowMD5 = true
	c.ApplyListenerOptions(custom)
	if !c.AllowMD5 || c.RestartKey == "" {
		t.Fatal("listener options must apply and contribute to the restart key")
	}
}

func TestProbe(t *testing.T) {
	upstream := newFakeUpstream(t, cleartextUpstream)
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	// with origin credentials the probe logs in; without them it can only connect
	for name, config := range map[string]Config{
		"login": terminatedConfig(upstream, testClientPass), "connect": testConfig(upstream),
	} {
		if err := config.Probe(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if user := upstream.lastStartup()[paramUser]; user != testUpstreamUser {
		t.Fatalf("the probe logged in as %q", user)
	}
	denied := terminatedConfig(upstream, testClientPass)
	denied.Upstream.Password = "wrong"
	if err := denied.Probe(ctx); err == nil || strings.Contains(err.Error(), testUpstreamUser) {
		t.Fatalf("expected a sanitized login failure, got %v", err)
	}
	for name, config := range map[string]Config{
		"login": terminatedConfig(upstream, testClientPass), "connect": testConfig(upstream),
	} {
		config.Upstream.Address = testUnusedAddress
		if err := config.Probe(ctx); err == nil || strings.Contains(err.Error(), testUnusedAddress) {
			t.Fatalf("%s: expected a sanitized connection failure, got %v", name, err)
		}
	}
}
