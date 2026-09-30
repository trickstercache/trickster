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
	"bytes"
	"context"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestReapIdlePrepared verifies that prepared statements abandoned without a
// close are released upstream once idle, while recently used ones survive.
func TestReapIdlePrepared(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())

	stale, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	// the fake upstream mints one fixed handle, so register a second, fresh
	// handle directly
	srv.registerPrepared([]byte("fresh-handle"), "SELECT 2")

	// age the first handle past the idle cutoff
	srv.paramMu.Lock()
	srv.prepared[string(stale.Handle)].lastAccess = time.Now().Add(-2 * DefaultPreparedIdleTTL)
	srv.paramMu.Unlock()

	if n := srv.ReapIdlePrepared(context.Background(), DefaultPreparedIdleTTL); n != 1 {
		t.Fatalf("ReapIdlePrepared() = %d, want 1", n)
	}
	if up.closePreparedCalls != 1 {
		t.Fatalf("upstream ClosePrepared calls = %d, want 1", up.closePreparedCalls)
	}
	srv.paramMu.Lock()
	remaining := len(srv.prepared)
	srv.paramMu.Unlock()
	if remaining != 1 {
		t.Fatalf("prepared registry size = %d, want 1", remaining)
	}
	// a second sweep reaps nothing
	if n := srv.ReapIdlePrepared(context.Background(), DefaultPreparedIdleTTL); n != 0 {
		t.Fatalf("second ReapIdlePrepared() = %d, want 0", n)
	}
}

// TestStreamIPCBytesContextCancel verifies the stream-feeding goroutine exits
// when the request context is canceled and no consumer ever reads the channel
// (a client disconnecting mid-stream).
func TestStreamIPCBytesContextCancel(t *testing.T) {
	b := buildTestIPC(t)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := streamIPCBytesWithRelease(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range 200 {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stream goroutine leaked after context cancellation")
}

func TestStreamIPCBytesHoldsBudgetUntilCompletion(t *testing.T) {
	b := buildTestIPC(t)
	srv := &Server{bufferBudget: newBufferBudget(int64(len(b)))}
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := srv.streamIPCBytes(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.streamIPCBytes(context.Background(), b); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v (code %s), want ResourceExhausted", err, status.Code(err))
	}
	cancel()
	for range 200 {
		if srv.bufferBudget.used.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stream did not release its buffering budget")
}

// TestServerCloseReleasesUpstream verifies Server.Close closes the upstream
// client so config-reload restarts don't leak connections.
func TestServerCloseReleasesUpstream(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, nil)
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if up.closeCalls != 1 {
		t.Fatalf("upstream Close calls = %d, want 1", up.closeCalls)
	}
}

// TestProtocolServerShutdownClosesUpstream verifies the listener lifecycle
// closes the flight server's upstream client exactly once.
func TestProtocolServerShutdownClosesUpstream(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	ps := NewProtocolServer(NewServer(up, nil), "lifecycle-test", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ps.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ps.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if up.closeCalls != 1 {
		t.Fatalf("upstream Close calls = %d, want 1", up.closeCalls)
	}
}

func checkedRecords(t *testing.T, mem *memory.CheckedAllocator, n int) (*arrow.Schema, []arrow.RecordBatch) {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{{Name: "v", Type: arrow.PrimitiveTypes.Int64}}, nil)
	records := make([]arrow.RecordBatch, n)
	for i := range records {
		b := array.NewRecordBuilder(mem, schema)
		b.Field(0).(*array.Int64Builder).AppendValues([]int64{int64(i), int64(i + 1)}, nil)
		records[i] = b.NewRecordBatch()
		b.Release()
	}
	return schema, records
}

func TestStreamRecords(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	schema, records := checkedRecords(t, mem, 3)
	srv := &Server{bufferBudget: newBufferBudget(1 << 20)}
	ctx, cancel := context.WithCancel(context.Background())
	got, ch, err := srv.streamRecords(ctx, schema, records)
	if err != nil || got != schema {
		t.Fatalf("schema %v, err %v", got, err)
	}
	var rows int64
	for chunk := range ch {
		rows += chunk.Data.NumRows()
		chunk.Data.Release()
	}
	if rows != 6 {
		t.Fatalf("rows = %d, want 6", rows)
	}
	if srv.bufferBudget.used.Load() == 0 {
		t.Fatal("the budget was released before the call completed")
	}
	cancel()
	for range 200 {
		if srv.bufferBudget.used.Load() == 0 {
			mem.AssertSize(t, 0)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stream did not release its buffering budget")
}

func TestStreamRecordsCanceled(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	schema, records := checkedRecords(t, mem, 3)
	ctx, cancel := context.WithCancel(context.Background())
	_, ch, err := (&Server{}).streamRecords(ctx, schema, records)
	if err != nil {
		t.Fatal(err)
	}
	chunk := <-ch
	chunk.Data.Release()
	cancel()
	for range ch {
		t.Fatal("a record was sent after the call was canceled")
	}
	// the records that were never sent are released by the stream
	mem.AssertSize(t, 0)
}

func TestStreamRecordsBudgetExhausted(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.NewGoAllocator())
	schema, records := checkedRecords(t, mem, 2)
	srv := &Server{bufferBudget: newBufferBudget(1)}
	if _, _, err := srv.streamRecords(context.Background(), schema, records); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v, want ResourceExhausted", err)
	}
	mem.AssertSize(t, 0)
}

func TestSchemaMemo(t *testing.T) {
	var sm schemaMemo
	header := flight.SerializeSchema(arrow.NewSchema([]arrow.Field{{Name: "v", Type: arrow.PrimitiveTypes.Int64}}, nil),
		memory.DefaultAllocator)
	a, err := sm.get(header)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sm.get(bytes.Clone(header))
	if err != nil || a != b {
		t.Fatalf("a second get of the same schema deserialized it again: %v", err)
	}
	if _, err := sm.get([]byte("not a schema")); err == nil {
		t.Fatal("expected an error for a header that isn't a schema")
	}
	for i := range maxMemoSchemas + 1 {
		s := arrow.NewSchema([]arrow.Field{{Name: "v" + strconv.Itoa(i), Type: arrow.PrimitiveTypes.Int64}}, nil)
		if _, err := sm.get(flight.SerializeSchema(s, memory.DefaultAllocator)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sm.m) > maxMemoSchemas {
		t.Fatalf("the memo holds %d schemas, over its bound", len(sm.m))
	}
}

func TestArrayBytesCountsDictionary(t *testing.T) {
	dt := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: arrow.BinaryTypes.String}
	b := array.NewDictionaryBuilder(memory.DefaultAllocator, dt).(*array.BinaryDictionaryBuilder)
	defer b.Release()
	for range 4 {
		if err := b.AppendString("a long dictionary word"); err != nil {
			t.Fatal(err)
		}
	}
	arr := b.NewArray()
	defer arr.Release()
	dict := arr.(*array.Dictionary).Dictionary()
	if got, min := arrayBytes(arr.Data()), arrayBytes(dict.Data()); got <= min {
		t.Fatalf("arrayBytes = %d, want more than the dictionary's own %d", got, min)
	}
}
