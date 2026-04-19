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
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// TestDoGetCatalogs verifies the catalogs proxy caches IPC bytes.
func TestDoGetCatalogs(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	for range 2 {
		schema, ch, err := srv.DoGetCatalogs(context.Background())
		if err != nil {
			t.Fatalf("DoGetCatalogs: %v", err)
		}
		if schema == nil {
			t.Fatal("expected schema")
		}
		for chunk := range ch {
			chunk.Data.Release()
		}
	}
	if up.catalogCalls != 1 {
		t.Errorf("expected 1 upstream catalog call (2nd cached), got %d", up.catalogCalls)
	}
}

// TestDoGetTables verifies tables proxy caches and uses a key that reflects filters.
func TestDoGetTables(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	cmd1 := fakeGetTables{}
	cmd2 := fakeGetTables{tableNameFilter: "cpu"}

	// same filter twice → 1 upstream call
	for range 2 {
		_, ch, err := srv.DoGetTables(context.Background(), cmd1)
		if err != nil {
			t.Fatalf("DoGetTables: %v", err)
		}
		for chunk := range ch {
			chunk.Data.Release()
		}
	}
	// different filter → another upstream call (different cache key)
	_, ch, err := srv.DoGetTables(context.Background(), cmd2)
	if err != nil {
		t.Fatalf("DoGetTables (filtered): %v", err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}

	if up.tablesCalls != 2 {
		t.Errorf("expected 2 tables calls (2nd cached, 3rd different key), got %d", up.tablesCalls)
	}
}

// TestDoGetDBSchemas verifies DB schemas proxy.
func TestDoGetDBSchemas(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	_, ch, err := srv.DoGetDBSchemas(context.Background(), fakeGetDBSchemas{})
	if err != nil {
		t.Fatalf("DoGetDBSchemas: %v", err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}
	if up.dbSchemaCalls != 1 {
		t.Errorf("expected 1 dbSchemas call, got %d", up.dbSchemaCalls)
	}
}

// TestDoGetTableTypes verifies table types proxy + caching.
func TestDoGetTableTypes(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	for range 3 {
		_, ch, err := srv.DoGetTableTypes(context.Background())
		if err != nil {
			t.Fatalf("DoGetTableTypes: %v", err)
		}
		for chunk := range ch {
			chunk.Data.Release()
		}
	}
	if up.tableTypesCalls != 1 {
		t.Errorf("expected 1 tableTypes call (cached), got %d", up.tableTypesCalls)
	}
}

// TestDoGetSqlInfo verifies SQL info proxy with info slice as cache key component.
func TestDoGetSqlInfo(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	_, ch, err := srv.DoGetSqlInfo(context.Background(), fakeGetSqlInfo{info: []uint32{1, 2, 3}})
	if err != nil {
		t.Fatalf("DoGetSqlInfo: %v", err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}
	if up.sqlInfoCalls != 1 {
		t.Errorf("expected 1 sqlInfo call, got %d", up.sqlInfoCalls)
	}
}

// TestPreparedStatement_Parameterized verifies that two Execute calls with
// different bound parameter values each hit upstream (distinct cache keys),
// while repeating the same params reuses the cache.
func TestPreparedStatement_Parameterized(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	// Create a prepared statement
	res, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT * FROM cpu WHERE cpu = ?"})
	if err != nil {
		t.Fatal(err)
	}

	// Build two distinct parameter records
	recA := buildParamRecord(t, "cpu-total")
	defer recA.Release()
	recB := buildParamRecord(t, "cpu0")
	defer recB.Release()

	cmd := fakePrepQuery{handle: res.Handle}

	// Bind params A, execute twice (2nd should cache-hit)
	if _, err := srv.DoPutPreparedStatementQuery(context.Background(), cmd,
		&fakeMessageReader{rec: recA}, nil); err != nil {
		t.Fatalf("DoPut A: %v", err)
	}
	for range 2 {
		_, ch, err := srv.DoGetPreparedStatement(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range ch {
			chunk.Data.Release()
		}
	}
	if up.executePreparedCalls != 1 {
		t.Errorf("phase A: expected 1 upstream execute, got %d", up.executePreparedCalls)
	}

	// Rebind with params B, execute — should NOT cache-hit since key differs
	if _, err := srv.DoPutPreparedStatementQuery(context.Background(), cmd,
		&fakeMessageReader{rec: recB}, nil); err != nil {
		t.Fatalf("DoPut B: %v", err)
	}
	_, ch, err := srv.DoGetPreparedStatement(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}
	if up.executePreparedCalls != 2 {
		t.Errorf("phase B: expected 2 upstream executes (different params), got %d",
			up.executePreparedCalls)
	}
	if up.setParamsCalls != 2 {
		t.Errorf("expected 2 SetParams calls, got %d", up.setParamsCalls)
	}
}

// buildParamRecord creates a single-column, single-row string record for use
// as a parameter binding.
func buildParamRecord(t *testing.T, val string) arrow.RecordBatch {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "p1", Type: arrow.BinaryTypes.String},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	b.Field(0).(*array.StringBuilder).Append(val)
	return b.NewRecord()
}

// fakeMessageReader adapts a single RecordBatch as a flight.MessageReader.
type fakeMessageReader struct {
	rec  arrow.RecordBatch
	done bool
}

func (f *fakeMessageReader) Next() bool {
	if f.done {
		return false
	}
	f.done = true
	return true
}
func (f *fakeMessageReader) RecordBatch() arrow.RecordBatch     { return f.rec }
func (f *fakeMessageReader) Record() arrow.RecordBatch          { return f.rec }
func (f *fakeMessageReader) Schema() *arrow.Schema              { return f.rec.Schema() }
func (f *fakeMessageReader) Err() error                         { return nil }
func (f *fakeMessageReader) Release()                           {}
func (f *fakeMessageReader) Retain()                            {}
func (f *fakeMessageReader) Read() (arrow.RecordBatch, error)   { return nil, nil }
func (f *fakeMessageReader) Chunk() flight.StreamChunk          { return flight.StreamChunk{Data: f.rec} }
func (f *fakeMessageReader) LatestFlightDescriptor() *flight.FlightDescriptor {
	return nil
}
func (f *fakeMessageReader) LatestAppMetadata() []byte { return nil }

// TestPreparedStatement covers the full prepare → execute → close cycle
// proxied to the upstream, including cache reuse on repeated Execute.
func TestPreparedStatement(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	// Create
	req := fakeCreatePrepReq{query: "SELECT * FROM cpu WHERE cpu = ?"}
	res, err := srv.CreatePreparedStatement(context.Background(), req)
	if err != nil {
		t.Fatalf("CreatePreparedStatement: %v", err)
	}
	if len(res.Handle) == 0 {
		t.Fatal("expected non-empty handle")
	}
	if up.prepareCalls != 1 {
		t.Errorf("expected 1 prepare call, got %d", up.prepareCalls)
	}

	// Execute twice — second should hit cache
	cmd := fakePrepQuery{handle: res.Handle}
	for range 2 {
		_, ch, err := srv.DoGetPreparedStatement(context.Background(), cmd)
		if err != nil {
			t.Fatalf("DoGetPreparedStatement: %v", err)
		}
		for chunk := range ch {
			chunk.Data.Release()
		}
	}
	if up.executePreparedCalls != 1 {
		t.Errorf("expected 1 upstream execute (2nd cached), got %d", up.executePreparedCalls)
	}

	// Close
	closeReq := fakeClosePrepReq{handle: res.Handle}
	if err := srv.ClosePreparedStatement(context.Background(), closeReq); err != nil {
		t.Errorf("ClosePreparedStatement: %v", err)
	}
	if up.closePreparedCalls != 1 {
		t.Errorf("expected 1 close call, got %d", up.closePreparedCalls)
	}
}

func TestGetFlightInfoPreparedStatement(t *testing.T) {
	srv := NewServer(&fakeUpstream{}, newMemCache())
	desc := &flight.FlightDescriptor{Cmd: []byte("some-cmd-bytes")}
	info, err := srv.GetFlightInfoPreparedStatement(context.Background(),
		fakePrepQuery{handle: []byte("h")}, desc)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Endpoint) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(info.Endpoint))
	}
}

// fakeCreatePrepReq implements flightsql.ActionCreatePreparedStatementRequest.
type fakeCreatePrepReq struct {
	query string
}

func (f fakeCreatePrepReq) GetQuery() string         { return f.query }
func (f fakeCreatePrepReq) GetTransactionId() []byte { return nil }

// fakePrepQuery implements flightsql.PreparedStatementQuery.
type fakePrepQuery struct {
	handle []byte
}

func (f fakePrepQuery) GetPreparedStatementHandle() []byte { return f.handle }

// fakeClosePrepReq implements flightsql.ActionClosePreparedStatementRequest.
type fakeClosePrepReq struct {
	handle []byte
}

func (f fakeClosePrepReq) GetPreparedStatementHandle() []byte { return f.handle }

// fakeGetTables implements flightsql.GetTables for tests.
type fakeGetTables struct {
	catalog              *string
	dbSchemaFilter       *string
	tableNameFilter      string
	tableTypes           []string
	includeSchema        bool
}

func (f fakeGetTables) GetCatalog() *string                { return f.catalog }
func (f fakeGetTables) GetDBSchemaFilterPattern() *string  { return f.dbSchemaFilter }
func (f fakeGetTables) GetTableNameFilterPattern() *string { return &f.tableNameFilter }
func (f fakeGetTables) GetTableTypes() []string            { return f.tableTypes }
func (f fakeGetTables) GetIncludeSchema() bool             { return f.includeSchema }

// fakeGetDBSchemas implements flightsql.GetDBSchemas.
type fakeGetDBSchemas struct {
	catalog        *string
	dbSchemaFilter *string
}

func (f fakeGetDBSchemas) GetCatalog() *string               { return f.catalog }
func (f fakeGetDBSchemas) GetDBSchemaFilterPattern() *string { return f.dbSchemaFilter }

// fakeGetSqlInfo implements flightsql.GetSqlInfo.
type fakeGetSqlInfo struct {
	info []uint32
}

func (f fakeGetSqlInfo) GetInfo() []uint32 { return f.info }

// unused interface guards
var _ flightsql.GetTables = fakeGetTables{}
var _ flightsql.GetDBSchemas = fakeGetDBSchemas{}
var _ flightsql.GetSqlInfo = fakeGetSqlInfo{}
