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
	"runtime"
	"testing"
	"time"
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
	srv.prepared[string(stale.Handle)].lastAccess =
		time.Now().Add(-2 * DefaultPreparedIdleTTL)
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
	if _, _, err := streamIPCBytes(ctx, b); err != nil {
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
