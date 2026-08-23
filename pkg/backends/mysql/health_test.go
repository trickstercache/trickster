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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
	"vitess.io/vitess/go/vt/vtenv"
	"vitess.io/vitess/go/vt/vttls"
)

type fakeHealthConnection struct {
	pingErr   error
	blockPing bool
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *fakeHealthConnection) Ping() error {
	if c.blockPing {
		<-c.closed
	}
	return c.pingErr
}

func (c *fakeHealthConnection) Close() {
	if c.closed != nil {
		c.closeOnce.Do(func() { close(c.closed) })
	}
}

func TestMySQLHealthProbeOutcomes(t *testing.T) {
	serverError := sqlerror.NewSQLError(sqlerror.ERUnknownError,
		sqlerror.SSUnknownSQLState, "sensitive server detail")
	tests := []struct {
		name       string
		connectErr error
		connection *fakeHealthConnection
		want       string
	}{
		{name: "success", connection: &fakeHealthConnection{}},
		{
			name:       "authentication failure",
			connectErr: sqlerror.NewSQLError(sqlerror.ERAccessDeniedError, sqlerror.SSAccessDeniedError, "password secret"),
			want:       "mysql authentication or database access failed",
		},
		{
			name:       "TLS failure",
			connectErr: sqlerror.NewSQLError(sqlerror.CRSSLConnectionError, sqlerror.SSUnknownSQLState, "certificate secret"),
			want:       "mysql TLS handshake or certificate verification failed",
		},
		{
			name: "refused connection",
			connectErr: sqlerror.NewSQLError(sqlerror.CRConnHostError,
				sqlerror.SSUnknownSQLState, "dial tcp: connect: connection refused"),
			want: "mysql origin refused the connection",
		},
		{
			name:       "server ping error",
			connection: &fakeHealthConnection{pingErr: serverError},
			want:       "mysql COM_PING failed with server error 1105",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := newMySQLHealthProbe(vtmysql.ConnParams{},
				func(context.Context, *vtmysql.ConnParams) (healthConnection, error) {
					return tc.connection, tc.connectErr
				})
			err := probe(context.Background())
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("probe error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("probe error exposed origin detail: %v", err)
			}
		})
	}
}

func TestMySQLHealthProbeTimeout(t *testing.T) {
	connection := &fakeHealthConnection{blockPing: true, closed: make(chan struct{})}
	probe := newMySQLHealthProbe(vtmysql.ConnParams{},
		func(context.Context, *vtmysql.ConnParams) (healthConnection, error) {
			return connection, nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := probe(ctx)
	if err == nil || err.Error() != "mysql health probe timed out" {
		t.Fatalf("probe error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("timed-out COM_PING did not return promptly")
	}
}

func TestMySQLHealthProbeRefusedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	listener.Close()
	probe := newMySQLHealthProbe(vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port,
		Uname: "origin", Pass: "origin-password",
	}, connectHealthOrigin)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probe(ctx); err == nil || err.Error() != "mysql origin refused the connection" {
		t.Fatalf("refused connection probe error = %v", err)
	}
}

func TestMySQLHealthProbeExecutesNativePing(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil),
		&testOriginHandler{env: vtenv.NewTestEnv()}, 0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	address := originListener.Addr().(*net.TCPAddr)
	probe := newMySQLHealthProbe(vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port,
		Uname: "origin", Pass: "origin-password",
	}, connectHealthOrigin)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probe(ctx); err != nil {
		t.Fatal(err)
	}

	authProbe := newMySQLHealthProbe(vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port,
		Uname: "origin", Pass: "wrong-password",
	}, connectHealthOrigin)
	if err := authProbe(ctx); err == nil || err.Error() != "mysql authentication or database access failed" {
		t.Fatalf("authentication probe error = %v", err)
	}

	tlsProbe := newMySQLHealthProbe(vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port,
		Uname: "origin", Pass: "origin-password", SslMode: vttls.Required,
	}, connectHealthOrigin)
	if err := tlsProbe(ctx); err == nil || err.Error() != "mysql TLS handshake or certificate verification failed" {
		t.Fatalf("TLS probe error = %v", err)
	}
}

func TestMySQLHealthProbeUsesConfiguredTLSAndCredentials(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://health-user:health-password@db.example:3307/analytics"
	o.TLS.InsecureSkipVerify = true
	params, err := upstreamConnParamsFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if params.Host != "db.example" || params.Port != 3307 ||
		params.Uname != "health-user" || params.Pass != "health-password" ||
		params.DbName != "analytics" || params.SslMode != vttls.Required {
		t.Fatalf("unexpected health connection params: %+v", params)
	}
}
