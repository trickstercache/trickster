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

package flight

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// startTestServer serves a ProtocolServer on an ephemeral port and returns its
// address. The server is shut down during test cleanup.
func startTestServer(t *testing.T, srv *Server) (*ProtocolServer, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ps := NewProtocolServer(srv, "test", nil)
	go func() { _ = ps.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = ps.Shutdown(ctx)
	})
	return ps, l.Addr().String()
}

// TestEndToEnd starts a Flight SQL server, connects a client, executes a
// query, and verifies the results stream back correctly through the full
// gRPC + Arrow IPC pipeline.
func TestEndToEnd(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	up := &fakeUpstream{ipcBytes: ipcBytes}
	srv := NewServer(up, newMemCache())
	_, addr := startTestServer(t, srv)

	client, err := flightsql.NewClientCtx(context.Background(), addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.Execute(ctx, "SELECT * FROM cpu")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(info.Endpoint) == 0 {
		t.Fatal("no endpoints returned")
	}

	reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
	if err != nil {
		t.Fatalf("doGet: %v", err)
	}
	defer reader.Release()

	var rows int64
	for reader.Next() {
		rec := reader.Record()
		rows += rec.NumRows()
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected 2 rows, got %d", rows)
	}
	if up.callCount != 1 {
		t.Errorf("expected 1 upstream call, got %d", up.callCount)
	}
}

// TestEndToEnd_Metadata verifies metadata RPCs (GetTables, GetCatalogs, etc.)
// flow through the full gRPC pipeline and reach the upstream.
func TestEndToEnd_Metadata(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	up := &fakeUpstream{ipcBytes: ipcBytes}
	srv := NewServer(up, newMemCache())
	_, addr := startTestServer(t, srv)

	client, err := flightsql.NewClientCtx(context.Background(), addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("GetCatalogs", func(t *testing.T) {
		info, err := client.GetCatalogs(ctx)
		if err != nil {
			t.Fatalf("GetCatalogs: %v", err)
		}
		if info == nil || len(info.Endpoint) == 0 {
			t.Fatal("missing endpoint")
		}
	})

	t.Run("GetTables", func(t *testing.T) {
		info, err := client.GetTables(ctx, &flightsql.GetTablesOpts{})
		if err != nil {
			t.Fatalf("GetTables: %v", err)
		}
		if info == nil || len(info.Endpoint) == 0 {
			t.Fatal("missing endpoint")
		}
	})

	t.Run("GetTableTypes", func(t *testing.T) {
		info, err := client.GetTableTypes(ctx)
		if err != nil {
			t.Fatalf("GetTableTypes: %v", err)
		}
		if info == nil || len(info.Endpoint) == 0 {
			t.Fatal("missing endpoint")
		}
	})

	t.Run("GetDBSchemas", func(t *testing.T) {
		info, err := client.GetDBSchemas(ctx, &flightsql.GetDBSchemasOpts{})
		if err != nil {
			t.Fatalf("GetDBSchemas: %v", err)
		}
		if info == nil || len(info.Endpoint) == 0 {
			t.Fatal("missing endpoint")
		}
	})

	t.Run("GetSqlInfo", func(t *testing.T) {
		info, err := client.GetSqlInfo(ctx, []flightsql.SqlInfo{
			flightsql.SqlInfoFlightSqlServerName,
		})
		if err != nil {
			t.Fatalf("GetSqlInfo: %v", err)
		}
		if info == nil || len(info.Endpoint) == 0 {
			t.Fatal("missing endpoint")
		}
	})
}

// TestProtocolServerShutdown verifies Serve returns nil after a clean
// Shutdown and that RPCs against the stopped server fail.
func TestProtocolServerShutdown(t *testing.T) {
	srv := NewServer(&fakeUpstream{ipcBytes: buildTestIPC(t)}, newMemCache())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ps := NewProtocolServer(srv, "shutdown-test", nil)
	if got := ps.ProtocolRestartKey(); got != "shutdown-test" {
		t.Fatalf("restart key = %q", got)
	}
	addr := l.Addr().String()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ps.Serve(l) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	if _, err := client.Execute(ctx, "SELECT * FROM cpu"); err != nil {
		t.Fatalf("execute before shutdown: %v", err)
	}
	client.Close()

	if err := ps.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned %v after clean shutdown, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}

	late, err := flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err == nil {
		defer late.Close()
		if _, rpcErr := late.Execute(ctx, "SELECT 1"); rpcErr == nil {
			t.Error("expected RPC to fail after Shutdown")
		}
	}
}
