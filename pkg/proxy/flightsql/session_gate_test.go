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

package flightsql

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const geoRefusal = "This resource is not available in your geographical area."

var _ listener.SessionGateUpdater = (*ProtocolServer)(nil)

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

func requirePermissionDenied(t *testing.T, err error) {
	t.Helper()
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied || s.Message() != geoRefusal {
		t.Fatalf("expected PermissionDenied with the message, got %v", err)
	}
}

func TestSessionGateJudgesEveryCall(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	ps, addr := startTestServer(t, NewServer(up, newMemCache()))
	gate := &testGate{}
	gate.refuse.Store(true)
	ps.UpdateSessionGate(gate)
	client, err := flightsql.NewClientCtx(context.Background(), addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Execute(ctx, "SELECT * FROM cpu")
	requirePermissionDenied(t, err)
	if got := *gate.last.Load(); got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("judged %v", got)
	}
	if up.callCount != 0 {
		t.Fatal("a refused call reached the upstream")
	}

	gate.refuse.Store(false)
	info, err := client.Execute(ctx, "SELECT * FROM cpu")
	if err != nil {
		t.Fatal(err)
	}
	// a stream on an admitted connection is judged too
	gate.refuse.Store(true)
	reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
	if err == nil {
		reader.Next()
		err = reader.Err()
		reader.Release()
	}
	requirePermissionDenied(t, err)

	ps.UpdateSessionGate(nil)
	if _, err := client.Execute(ctx, "SELECT * FROM cpu"); err != nil {
		t.Fatalf("call after the gate was removed: %v", err)
	}
}
