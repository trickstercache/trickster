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

package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResponseTrailers proves the gRPC-shaped case: a status carried only in
// trailers survives the proxy hop. Without trailer forwarding a gRPC client
// sees message frames followed by a clean EOF and no status.
func TestResponseTrailers(t *testing.T) {
	var originTE string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originTE = r.Header.Get("Te")
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("frame"))
		w.Header().Set("Grpc-Status", "0")
		w.Header().Set("Grpc-Message", "OK")
	}))
	defer origin.Close()

	h := configHarness(t, addPassthroughBackend("grpcproxy", origin.URL))
	h.start(t)

	req, err := http.NewRequest(http.MethodGet, "http://"+h.BaseAddr+"/grpcproxy/rpc", nil)
	require.NoError(t, err)
	req.Header.Set("Te", "trailers")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "frame", string(body))

	require.Equal(t, "trailers", originTE,
		"Te: trailers must be restored on the outbound request after hop-header stripping")
	require.Equal(t, "0", resp.Trailer.Get("Grpc-Status"),
		"trailers must reach the client (trailers seen: %v)", resp.Trailer)
	require.Equal(t, "OK", resp.Trailer.Get("Grpc-Message"))
}

// TestStreamingResponseFlush proves an unknown-length response is delivered
// incrementally rather than held until net/http's buffer fills.
func TestStreamingResponseFlush(t *testing.T) {
	release := make(chan struct{})
	releaseHeld := sync.OnceFunc(func() { close(release) })
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: first\n\n"))
		http.NewResponseController(w).Flush()
		<-release
		w.Write([]byte("data: second\n\n"))
		http.NewResponseController(w).Flush()
	}))
	t.Cleanup(origin.Close)
	// runs before origin.Close, which would otherwise wait forever on a handler a failed test never released
	t.Cleanup(releaseHeld)

	h := configHarness(t, addPassthroughBackend("sseproxy", origin.URL))
	h.start(t)

	// a proxy that buffers never delivers the first event, so the read is bounded
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+h.BaseAddr+"/sseproxy/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// the first event must arrive before the origin has sent the second, which
	// only holds if the proxy flushes rather than buffering
	buf := make([]byte, len("data: first\n\n"))
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\n", string(buf))
	releaseHeld()

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: second\n\n", string(rest))
}
