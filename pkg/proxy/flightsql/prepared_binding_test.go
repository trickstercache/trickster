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
	"sync"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// bindingUpstream echoes the parameter binding that was live when an execution
// ran, so a cache entry populated under a mismatched binding is detectable
// after the fact.
type bindingUpstream struct {
	fakeUpstream
	mu sync.Mutex
	// bound is the value most recently bound upstream.
	bound string
	// payloads maps a bound value to the IPC response it produces.
	payloads map[string][]byte
}

func (b *bindingUpstream) SetPreparedStatementParams(_ context.Context,
	_ []byte, params arrow.RecordBatch,
) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if params == nil {
		b.bound = ""
		return nil
	}
	b.bound = params.Column(0).ValueStr(0)
	return nil
}

func (b *bindingUpstream) ExecutePrepared(_ context.Context, _ []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.payloads[b.bound], nil
}

// taggedIPC encodes a single-row response whose only cell is value, so a
// decoded response names the binding that produced it.
func taggedIPC(t *testing.T, value string) []byte {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "bound", Type: arrow.BinaryTypes.String},
	}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	builder.Field(0).(*array.StringBuilder).Append(value)
	record := builder.NewRecordBatch()
	defer record.Release()
	data, err := EncodeRecords(schema, []arrow.RecordBatch{record})
	if err != nil {
		t.Fatalf("EncodeRecords: %v", err)
	}
	return data
}

// boundValue reads back the binding a streamed response was produced under.
func boundValue(ch <-chan flight.StreamChunk) string {
	value := ""
	for chunk := range ch {
		if chunk.Data.NumRows() > 0 {
			value = chunk.Data.Column(0).ValueStr(0)
		}
		chunk.Data.Release()
	}
	return value
}

// bindParams binds one parameter value against a handle.
func bindParams(t *testing.T, srv *Server, cmd fakePrepQuery, value string) error {
	t.Helper()
	rec := buildParamRecord(t, value)
	defer rec.Release()
	_, err := srv.DoPutPreparedStatementQuery(context.Background(), cmd,
		&fakeMessageReader{rec: rec}, nil)
	return err
}

// multiBatchReader yields several parameter record batches, as a Flight SQL
// client streaming bindings would.
type multiBatchReader struct {
	fakeMessageReader
	remaining int
}

func (m *multiBatchReader) Next() bool {
	if m.remaining == 0 {
		return false
	}
	m.remaining--
	return true
}

// TestPreparedBindingIsAtomicWithExecution verifies a bind racing an execution
// can never cache one binding's response under another's parameter hash. After
// a storm of concurrent binds and executions, every parameter's cache entry
// must still decode to that parameter's own response.
func TestPreparedBindingIsAtomicWithExecution(t *testing.T) {
	values := []string{"a", "b", "c", "d"}
	up := &bindingUpstream{payloads: make(map[string][]byte)}
	for _, value := range values {
		up.payloads[value] = taggedIPC(t, value)
	}
	up.payloads[""] = taggedIPC(t, "")
	srv := NewServer(up, newMemCache())
	res, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT * FROM cpu WHERE host = ?"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := fakePrepQuery{handle: res.Handle}

	var wg sync.WaitGroup
	for round := range 200 {
		value := values[round%len(values)]
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := bindParams(t, srv, cmd, value); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			_, ch, err := srv.DoGetPreparedStatement(context.Background(), cmd)
			if err != nil {
				t.Error(err)
				return
			}
			boundValue(ch)
		}()
	}
	wg.Wait()

	// each parameter's cache entry must hold that parameter's own response
	for _, value := range values {
		if err := bindParams(t, srv, cmd, value); err != nil {
			t.Fatal(err)
		}
		_, ch, err := srv.DoGetPreparedStatement(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		if got := boundValue(ch); got != value {
			t.Fatalf("parameter %q served the response bound for %q", value, got)
		}
	}
}

// TestPreparedBindingSerializesWithExecution verifies a bind cannot interleave
// with an in-flight execution of the same handle.
func TestPreparedBindingSerializesWithExecution(t *testing.T) {
	up := &bindingUpstream{payloads: map[string][]byte{"": buildTestIPC(t)}}
	srv := NewServer(up, newMemCache())
	res, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := fakePrepQuery{handle: res.Handle}
	meta := srv.preparedEntry(res.Handle)

	// hold the handle's lock and confirm a bind blocks until it is released
	meta.mu.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		if err := bindParams(t, srv, cmd, "x"); err != nil {
			t.Error(err)
		}
		close(done)
	}()
	<-started
	select {
	case <-done:
		t.Fatal("bind proceeded while the handle was locked")
	default:
	}
	meta.mu.Unlock()
	<-done
}

// TestPreparedMultiBatchBindingRejected verifies a streamed multi-batch
// binding is refused rather than silently bound from its first batch.
func TestPreparedMultiBatchBindingRejected(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := NewServer(up, newMemCache())
	res, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT * FROM cpu WHERE host = ?"})
	if err != nil {
		t.Fatal(err)
	}
	rec := buildParamRecord(t, "a")
	defer rec.Release()
	reader := &multiBatchReader{
		fakeMessageReader: fakeMessageReader{rec: rec}, remaining: 2,
	}
	_, err = srv.DoPutPreparedStatementQuery(context.Background(),
		fakePrepQuery{handle: res.Handle}, reader, nil)
	if err == nil {
		t.Fatal("expected a multi-batch binding to be refused")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("code = %s, want Unimplemented", status.Code(err))
	}
	if up.setParamsCalls != 0 {
		t.Errorf("bound %d parameter batches upstream, want 0", up.setParamsCalls)
	}
}

// TestPreparedEmptyBindingClearsUpstream verifies a DoPut with no batches
// clears the upstream binding rather than leaving the previous one active.
func TestPreparedEmptyBindingClearsUpstream(t *testing.T) {
	up := &bindingUpstream{payloads: map[string][]byte{"a": buildTestIPC(t)}}
	srv := NewServer(up, newMemCache())
	res, err := srv.CreatePreparedStatement(context.Background(),
		fakeCreatePrepReq{query: "SELECT * FROM cpu WHERE host = ?"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := fakePrepQuery{handle: res.Handle}
	if err := bindParams(t, srv, cmd, "a"); err != nil {
		t.Fatal(err)
	}
	rec := buildParamRecord(t, "a")
	defer rec.Release()
	if _, err := srv.DoPutPreparedStatementQuery(context.Background(), cmd,
		&fakeMessageReader{rec: rec, done: true}, nil); err != nil {
		t.Fatal(err)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.bound != "" {
		t.Errorf("upstream binding = %q, want cleared", up.bound)
	}
}
