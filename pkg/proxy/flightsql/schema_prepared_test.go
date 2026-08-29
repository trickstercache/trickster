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

package flightsql

import (
	"context"
	"fmt"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetSchemaRPCsCacheFirst verifies the schema RPCs ADBC drivers probe on
// connect proxy to the upstream and cache under the tenant scope.
func TestGetSchemaRPCsCacheFirst(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())
	ctx := context.Background()

	wantBytes, err := up.serializedSchema()
	if err != nil {
		t.Fatal(err)
	}
	want, err := flight.DeserializeSchema(wantBytes, memory.DefaultAllocator)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		result, err := srv.GetSchemaStatement(ctx,
			fakeCreatePrepReq{query: "SELECT * FROM cpu"}, nil)
		if err != nil {
			t.Fatalf("GetSchemaStatement: %v", err)
		}
		got, err := flight.DeserializeSchema(result.GetSchema(), memory.DefaultAllocator)
		if err != nil || !got.Equal(want) {
			t.Fatalf("statement schema = %v, %v", got, err)
		}
	}
	if up.executeSchemaCalls != 1 {
		t.Fatalf("statement schema made %d upstream calls, want 1", up.executeSchemaCalls)
	}

	created, err := srv.CreatePreparedStatement(ctx,
		fakeCreatePrepReq{query: "SELECT * FROM cpu"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := srv.GetSchemaPreparedStatement(ctx,
			fakePrepQuery{handle: created.Handle}, nil)
		if err != nil {
			t.Fatalf("GetSchemaPreparedStatement: %v", err)
		}
		got, err := flight.DeserializeSchema(result.GetSchema(), memory.DefaultAllocator)
		if err != nil || !got.Equal(want) {
			t.Fatalf("prepared schema = %v, %v", got, err)
		}
	}
	if up.preparedSchemaCalls != 1 {
		t.Fatalf("prepared schema made %d upstream calls, want 1", up.preparedSchemaCalls)
	}

	up.returnErr = fmt.Errorf("origin down")
	if _, err := srv.GetSchemaStatement(ctx,
		fakeCreatePrepReq{query: "SELECT * FROM mem"}, nil); err == nil {
		t.Fatal("schema fetch error was swallowed")
	}
	up.returnErr = nil
}

// TestGetSchemaFallsBackToExecution covers upstreams that do not implement
// the schema RPCs (InfluxDB 3 Core among them): the schema is derived by
// executing the statement through the object tier.
func TestGetSchemaFallsBackToExecution(t *testing.T) {
	up := &fakeUpstream{
		ipcBytes:  buildTestIPC(t),
		schemaErr: status.Error(codes.Unimplemented, "Not yet implemented: get_schema"),
	}
	srv := NewServer(up, newMemCache())
	ctx := context.Background()

	wantBytes, err := up.serializedSchema()
	if err != nil {
		t.Fatal(err)
	}
	want, err := flight.DeserializeSchema(wantBytes, memory.DefaultAllocator)
	if err != nil {
		t.Fatal(err)
	}

	result, err := srv.GetSchemaStatement(ctx,
		fakeCreatePrepReq{query: "SELECT * FROM cpu"}, nil)
	if err != nil {
		t.Fatalf("GetSchemaStatement fallback: %v", err)
	}
	got, err := flight.DeserializeSchema(result.GetSchema(), memory.DefaultAllocator)
	if err != nil || !got.Equal(want) {
		t.Fatalf("fallback statement schema = %v, %v", got, err)
	}
	if up.executeCalls != 1 {
		t.Fatalf("fallback made %d executions, want 1", up.executeCalls)
	}
	// the executed response landed in the object tier: a following
	// DoGetStatement for the same text is a cache hit
	executeRowCount := up.executeCalls
	_, ch, err := srv.DoGetStatement(ctx, fakeStatementTicket{handle: []byte("SELECT * FROM cpu")})
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}
	if up.executeCalls != executeRowCount {
		t.Fatalf("post-probe execution missed the object tier: %d calls", up.executeCalls)
	}

	// the prepared variant falls back through the handle's statement text
	created, err := srv.CreatePreparedStatement(ctx,
		fakeCreatePrepReq{query: "SELECT * FROM cpu"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := srv.GetSchemaPreparedStatement(ctx,
		fakePrepQuery{handle: created.Handle}, nil)
	if err != nil {
		t.Fatalf("GetSchemaPreparedStatement fallback: %v", err)
	}
	got, err = flight.DeserializeSchema(prepared.GetSchema(), memory.DefaultAllocator)
	if err != nil || !got.Equal(want) {
		t.Fatalf("fallback prepared schema = %v, %v", got, err)
	}

	// a non-Unimplemented upstream schema failure still surfaces
	up.schemaErr = status.Error(codes.Internal, "schema exploded")
	if _, err := srv.GetSchemaStatement(ctx,
		fakeCreatePrepReq{query: "SELECT * FROM mem"}, nil); err == nil {
		t.Fatal("internal schema error was swallowed by the fallback")
	}
}

func drainPrepared(t *testing.T, srv *Server, handle []byte) int {
	t.Helper()
	_, ch, err := srv.DoGetPreparedStatement(context.Background(),
		fakePrepQuery{handle: handle})
	if err != nil {
		t.Fatalf("DoGetPreparedStatement: %v", err)
	}
	rows := 0
	for chunk := range ch {
		rows += int(chunk.Data.NumRows())
		chunk.Data.Release()
	}
	return rows
}

// TestPreparedStatementDeltaTier verifies parameterless prepared statements
// share the statement query tiers — delta caching included — while
// parameterized executions keep the whole-response path keyed by param hash.
func TestPreparedStatementDeltaTier(t *testing.T) {
	up := &fakeUpstream{executeFn: rangedUpstream(t), ipcBytes: buildTestIPC(t)}
	srv := newDeltaTestServer(t, up)
	ctx := context.Background()

	query := fmt.Sprintf(deltaQuery, 0, 600)
	created, err := srv.CreatePreparedStatement(ctx, fakeCreatePrepReq{query: query})
	if err != nil {
		t.Fatal(err)
	}

	// two parameterless executions: one upstream statement fetch, zero
	// prepared executions
	if rows := drainPrepared(t, srv, created.Handle); rows != 20 {
		t.Fatalf("prepared delta rows = %d, want 20", rows)
	}
	drainPrepared(t, srv, created.Handle)
	if up.executeCalls != 1 || up.executePreparedCalls != 0 {
		t.Fatalf("parameterless prepared made %d execute / %d prepared calls",
			up.executeCalls, up.executePreparedCalls)
	}

	// a plain Execute of the same text shares the delta cache entry
	if rows := len(executeRows(t, srv, query)); rows != 20 {
		t.Fatalf("statement rows = %d, want 20", rows)
	}
	if up.executeCalls != 1 {
		t.Fatalf("statement execution did not share the prepared cache: %d calls",
			up.executeCalls)
	}

	// binding parameters routes through the upstream prepared execution
	record := buildParamRecord(t, "cpu0")
	defer record.Release()
	if _, err := srv.DoPutPreparedStatementQuery(ctx,
		fakePrepQuery{handle: created.Handle},
		&fakeMessageReader{rec: record}, nil); err != nil {
		t.Fatal(err)
	}
	drainPrepared(t, srv, created.Handle)
	if up.executePreparedCalls != 1 {
		t.Fatalf("parameterized prepared made %d prepared calls, want 1",
			up.executePreparedCalls)
	}

	// clearing parameters restores the shared statement tiers
	if _, err := srv.DoPutPreparedStatementQuery(ctx,
		fakePrepQuery{handle: created.Handle},
		&fakeMessageReader{done: true}, nil); err != nil {
		t.Fatal(err)
	}
	drainPrepared(t, srv, created.Handle)
	if up.executeCalls != 1 || up.executePreparedCalls != 1 {
		t.Fatalf("cleared-params prepared made %d execute / %d prepared calls",
			up.executeCalls, up.executePreparedCalls)
	}
}

// TestPreparedStatementWithoutDeltaKeepsLegacyPath verifies servers built
// without a delta tier execute prepared statements upstream as before.
func TestPreparedStatementWithoutDeltaKeepsLegacyPath(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())
	created, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT * FROM cpu"})
	if err != nil {
		t.Fatal(err)
	}
	drainPrepared(t, srv, created.Handle)
	drainPrepared(t, srv, created.Handle)
	if up.executePreparedCalls != 1 || up.executeCalls != 0 {
		t.Fatalf("legacy prepared path made %d prepared / %d execute calls",
			up.executePreparedCalls, up.executeCalls)
	}
}
