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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// fakeUpstream is a simple UpstreamClient for tests that returns pre-canned IPC bytes.
// All RPC methods return the same ipcBytes payload — tests only care about call
// counts and cache behavior, not differentiated metadata content.
type fakeUpstream struct {
	mu        sync.Mutex
	callCount int
	ipcBytes  []byte
	lastQuery string
	returnErr error

	// per-method counters so tests can verify specific RPCs were hit
	executeCalls         int
	catalogCalls         int
	dbSchemaCalls        int
	tablesCalls          int
	tableTypesCalls      int
	sqlInfoCalls         int
	prepareCalls         int
	setParamsCalls       int
	executePreparedCalls int
	closePreparedCalls   int
}

func (f *fakeUpstream) Execute(_ context.Context, query string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.executeCalls++
	f.lastQuery = query
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) GetCatalogs(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.catalogCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) GetDBSchemas(_ context.Context, _ *flightsql.GetDBSchemasOpts) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.dbSchemaCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) GetTables(_ context.Context, _ *flightsql.GetTablesOpts) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.tablesCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) GetTableTypes(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.tableTypesCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) GetSqlInfo(_ context.Context, _ []flightsql.SqlInfo) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.sqlInfoCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) PrepareStatement(_ context.Context, _ string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.prepareCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return []byte("fake-handle"), nil
}

func (f *fakeUpstream) SetPreparedStatementParams(_ context.Context, _ []byte, _ arrow.RecordBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setParamsCalls++
	return nil
}

func (f *fakeUpstream) ExecutePrepared(_ context.Context, _ []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.executePreparedCalls++
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.ipcBytes, nil
}

func (f *fakeUpstream) ClosePrepared(_ context.Context, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closePreparedCalls++
	return nil
}

func (f *fakeUpstream) Close() error { return nil }

// memCache is a simple in-memory implementation of Cache for tests.
type memCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCache() *memCache {
	return &memCache{data: make(map[string][]byte)}
}

func (c *memCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[key]
	return b, ok
}

func (c *memCache) Set(key string, data []byte, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = data
}

// buildTestIPC creates a small Arrow record and encodes it to IPC bytes.
func buildTestIPC(t *testing.T) []byte {
	t.Helper()
	mem := memory.DefaultAllocator
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: arrow.FixedWidthTypes.Timestamp_ns},
		{Name: "value", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	b := array.NewRecordBuilder(mem, schema)
	defer b.Release()
	b.Field(0).(*array.TimestampBuilder).AppendValues(
		[]arrow.Timestamp{1000000000, 2000000000}, nil)
	b.Field(1).(*array.Float64Builder).AppendValues(
		[]float64{1.5, 2.7}, nil)
	rec := b.NewRecord()
	defer rec.Release()
	data, err := EncodeRecords(schema, []arrow.RecordBatch{rec})
	if err != nil {
		t.Fatalf("EncodeRecords: %v", err)
	}
	return data
}

func TestDoGetStatement_UpstreamHit(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	up := &fakeUpstream{ipcBytes: ipcBytes}
	cache := newMemCache()
	srv := NewServer(up, cache)

	sqt := statementTicket("SELECT * FROM cpu")

	schema, ch, err := srv.DoGetStatement(context.Background(), sqt)
	if err != nil {
		t.Fatalf("DoGetStatement: %v", err)
	}
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
	if len(schema.Fields()) != 2 {
		t.Errorf("expected 2 fields, got %d", len(schema.Fields()))
	}

	rowCount := 0
	for chunk := range ch {
		rowCount += int(chunk.Data.NumRows())
		chunk.Data.Release()
	}
	if rowCount != 2 {
		t.Errorf("expected 2 rows, got %d", rowCount)
	}
	if up.callCount != 1 {
		t.Errorf("expected 1 upstream call, got %d", up.callCount)
	}
}

func TestDoGetStatement_CacheHit(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	up := &fakeUpstream{ipcBytes: ipcBytes}
	cache := newMemCache()
	srv := NewServer(up, cache)

	query := "SELECT * FROM cpu"
	sqt := statementTicket(query)

	// First call populates cache.
	_, ch, err := srv.DoGetStatement(context.Background(), sqt)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}

	// Second call should hit cache, not upstream.
	_, ch2, err := srv.DoGetStatement(context.Background(), sqt)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch2 {
		chunk.Data.Release()
	}

	if up.callCount != 1 {
		t.Errorf("expected 1 upstream call (2nd should hit cache), got %d", up.callCount)
	}
}

func TestDoGetStatement_UpstreamError(t *testing.T) {
	up := &fakeUpstream{returnErr: fmt.Errorf("boom")}
	cache := newMemCache()
	srv := NewServer(up, cache)

	sqt := statementTicket("SELECT 1")

	_, _, err := srv.DoGetStatement(context.Background(), sqt)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetFlightInfoStatement(t *testing.T) {
	srv := NewServer(&fakeUpstream{}, newMemCache())
	cmd := fakeStatementQuery{query: "SELECT 1"}
	info, err := srv.GetFlightInfoStatement(context.Background(), cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || len(info.Endpoint) != 1 {
		t.Fatal("expected 1 endpoint")
	}
	if info.Endpoint[0].Ticket == nil {
		t.Fatal("expected ticket")
	}
}

// fakeStatementQuery implements flightsql.StatementQuery for tests.
type fakeStatementQuery struct {
	query string
}

func (f fakeStatementQuery) GetQuery() string         { return f.query }
func (f fakeStatementQuery) GetTransactionId() []byte { return nil }

// fakeStatementTicket implements flightsql.StatementQueryTicket for tests.
type fakeStatementTicket struct {
	handle []byte
}

func (f fakeStatementTicket) GetStatementHandle() []byte { return f.handle }

func statementTicket(query string) flightsql.StatementQueryTicket {
	return fakeStatementTicket{handle: []byte(query)}
}

func TestRoundTripIPC(t *testing.T) {
	ipcBytes := buildTestIPC(t)
	schema, recs, err := DecodeRecords(ipcBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	if len(schema.Fields()) != 2 {
		t.Errorf("schema field count: got %d, want 2", len(schema.Fields()))
	}
	var total int64
	for _, r := range recs {
		total += r.NumRows()
	}
	if total != 2 {
		t.Errorf("total rows: got %d, want 2", total)
	}
}
