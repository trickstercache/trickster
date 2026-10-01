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
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/stretchr/testify/require"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
	"vitess.io/vitess/go/vt/vtenv"
)

const geoRefusal = "This resource is not available in your geographical area."

var _ listener.SessionGateUpdater = (*ProtocolServer)(nil)

type testGate struct { // refuses every session while refuse is set
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

func TestProtocolServerSessionGate(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil),
		&testOriginHandler{env: vtenv.NewTestEnv()}, 0, 0, false, false, 0, 0, false)
	require.NoError(t, err)
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{
			Host: "127.0.0.1", Port: originListener.Addr().(*net.TCPAddr).Port,
			Uname: "origin", Pass: "origin-password",
		},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second,
		BackendName:     "mysql-geo-test",
	})
	require.NoError(t, err)
	gate := &testGate{}
	gate.refuse.Store(true)
	server.UpdateSessionGate(gate)
	go func() { _ = server.Serve(proxyListener) }()
	defer func() { _ = server.Shutdown(context.Background()) }()

	port := proxyListener.Addr().(*net.TCPAddr).Port
	connect := func(password string) (*vtmysql.Conn, error) {
		return vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
			Host: "127.0.0.1", Port: port, Uname: "client", Pass: password,
		})
	}

	// a refused session gets ER_HOST_NOT_PRIVILEGED with the message, before its credential is checked
	_, err = connect("the wrong password")
	var sqlErr *sqlerror.SQLError
	require.True(t, errors.As(err, &sqlErr), "%v", err)
	require.Equal(t, sqlerror.ErrorCode(1130), sqlErr.Num)
	require.Equal(t, sqlerror.SSUnknownSQLState, sqlErr.State)
	require.Contains(t, sqlErr.Message, geoRefusal)
	require.Equal(t, netip.MustParseAddr("127.0.0.1"), *gate.last.Load())

	// an allowed session is served, and a reload that removes the gate reaches the next session only
	gate.refuse.Store(false)
	client, err := connect("client-password")
	require.NoError(t, err)
	defer client.Close()
	gate.refuse.Store(true)
	_, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	require.NoError(t, err, "an admitted session is not judged again")
	server.UpdateSessionGate(nil)
	other, err := connect("client-password")
	require.NoError(t, err)
	other.Close()
}
