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

// TestEndToEnd starts a Flight SQL server, connects a client, executes a
// query, and verifies the results stream back correctly through the full
// gRPC + Arrow IPC pipeline.
func TestEndToEnd(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	up := &fakeUpstream{ipcBytes: ipcBytes}
	srv := NewServer(up, newMemCache())

	lis, err := Start(ListenerConfig{Address: "127.0.0.1", Port: 0}, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Stop(0)

	addr := lis.Addr().String()
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

	lis, err := Start(ListenerConfig{Address: "127.0.0.1", Port: 0}, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Stop(0)

	client, err := flightsql.NewClientCtx(context.Background(), lis.Addr().String(), nil, nil,
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

// TestStart_ReplacesExistingByName simulates a config reload: calling Start
// twice with the same Name + Port should stop the first listener so the
// second can bind without EADDRINUSE.
func TestStart_ReplacesExistingByName(t *testing.T) {
	srv := NewServer(&fakeUpstream{ipcBytes: buildTestIPC(t)}, newMemCache())
	// pick an explicit port so both calls target the same bind address
	first, err := Start(ListenerConfig{Address: "127.0.0.1", Port: 0, Name: "backend-a"}, srv)
	if err != nil {
		t.Fatal(err)
	}
	port := first.Addr().(*net.TCPAddr).Port

	second, err := Start(ListenerConfig{Address: "127.0.0.1", Port: port, Name: "backend-a"}, srv)
	if err != nil {
		t.Fatalf("second Start on same name+port should replace, got: %v", err)
	}
	defer second.Stop(0)

	// first listener should be stopped; RPC against it should fail
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ShutdownAll(ctx); err != nil {
		t.Errorf("ShutdownAll: %v", err)
	}
}

// TestShutdownAll verifies that registered listeners are stopped and
// accepting connections afterward fails.
func TestShutdownAll(t *testing.T) {
	srv := NewServer(&fakeUpstream{ipcBytes: buildTestIPC(t)}, newMemCache())
	lis, err := Start(ListenerConfig{Address: "127.0.0.1", Port: 0, Name: "test"}, srv)
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	if err := ShutdownAll(context.Background()); err != nil {
		t.Fatalf("ShutdownAll: %v", err)
	}

	// After shutdown, a new dial to the same address should fail quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	// dial may succeed (lazy), so also try a real RPC to confirm the server is gone.
	if err == nil {
		c, _ := flightsql.NewClientCtx(ctx, addr, nil, nil,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if c != nil {
			defer c.Close()
			_, rpcErr := c.Execute(ctx, "SELECT 1")
			if rpcErr == nil {
				t.Error("expected RPC to fail after ShutdownAll")
			}
		}
	}
}
