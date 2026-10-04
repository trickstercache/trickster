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

package pgwire

import (
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/jackc/pgx/v5/pgconn"
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
	upstream := newFakeUpstream(t, cleartextUpstream)
	server, address := startServer(t, terminatedConfig(upstream, testClientPass))
	gate := &testGate{}
	gate.refuse.Store(true)
	server.UpdateSessionGate(gate)

	// a refused session gets FATAL 28000 with the message, before any credential is checked
	_, err := dial(t, address, testClientUser, "wrong-password")
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != sqlstateInvalidAuthSpec || pgErr.Severity != severityFatal || pgErr.Message != geoRefusal {
		t.Fatalf("refused session: %v", err)
	}
	if got := *gate.last.Load(); got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("judged %v", got)
	}
	if upstream.lastStartup() != nil {
		t.Fatal("the origin was contacted for a refused session")
	}

	// an admitted session is served and not judged again; a reload reaches the next session
	gate.refuse.Store(false)
	conn := mustDial(t, address, testClientUser, testClientPass)
	gate.refuse.Store(true)
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("admitted session: %d rows, %v", rows, err)
	}
	server.UpdateSessionGate(nil)
	mustDial(t, address, testClientUser, testClientPass)
}
