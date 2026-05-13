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

package fanout

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func mkTarget(_ string, h http.HandlerFunc) *pool.Target {
	return pool.NewTarget(h, &healthcheck.Status{}, nil)
}

func newParentGET(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	require.NoError(t, err)
	return r
}

func newParentPOST(t *testing.T, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "http://trickstercache.org/api/v1/query_range", strings.NewReader(body))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestAllOrderedResults(t *testing.T) {
	const n = 5
	targets := make(pool.Targets, n)
	for i := range n {
		body := fmt.Sprintf("slot-%d", i)
		targets[i] = mkTarget(body, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		})
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, n)
	for i := range n {
		require.Equal(t, i, results[i].Index)
		require.False(t, results[i].Failed, "slot %d failed: %v", i, results[i].Err)
		require.NotNil(t, results[i].Capture)
		require.Equal(t, fmt.Sprintf("slot-%d", i), string(results[i].Capture.Body()))
	}
}

func TestAllCtxCancelPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 3)
	mk := func() *pool.Target {
		return mkTarget("block", func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-r.Context().Done()
			w.WriteHeader(http.StatusGatewayTimeout)
		})
	}
	targets := pool.Targets{mk(), mk(), mk()}
	parent := newParentGET(t)

	done := make(chan []Result, 1)
	go func() {
		results, _ := All(ctx, parent, targets, Config{Mechanism: "test"})
		done <- results
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("handler never started")
		}
	}
	cancel()
	select {
	case results := <-done:
		require.Len(t, results, 3)
	case <-time.After(3 * time.Second):
		t.Fatal("All did not return after ctx cancel")
	}
}

func TestAllPanicRecovered(t *testing.T) {
	targets := pool.Targets{
		mkTarget("ok-0", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok-0"))
		}),
		mkTarget("panic", func(_ http.ResponseWriter, _ *http.Request) {
			panic("boom")
		}),
		mkTarget("ok-2", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok-2"))
		}),
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, 3)
	require.False(t, results[0].Failed)
	require.Equal(t, "ok-0", string(results[0].Capture.Body()))
	require.True(t, results[1].Failed)
	require.Nil(t, results[1].Capture)
	require.False(t, results[2].Failed)
	require.Equal(t, "ok-2", string(results[2].Capture.Body()))
}

func TestAllConcurrencyLimit(t *testing.T) {
	const n = 10
	const limit = 2
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	targets := make(pool.Targets, n)
	for i := range n {
		targets[i] = mkTarget("x", func(w http.ResponseWriter, _ *http.Request) {
			cur := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			w.WriteHeader(http.StatusOK)
		})
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test", ConcurrencyLimit: limit})
	require.Len(t, results, n)
	require.LessOrEqual(t, int(maxSeen.Load()), limit, "max in-flight exceeded limit")
	require.Greater(t, int(maxSeen.Load()), 0, "no concurrency observed")
}

func TestAllCaptureBound(t *testing.T) {
	const max = 1024
	big := bytes.Repeat([]byte("a"), 100*1024)
	targets := pool.Targets{
		mkTarget("big", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(big)
		}),
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test", MaxCaptureBytes: max})
	require.Len(t, results, 1)
	require.True(t, results[0].Failed, "truncation must surface as failure")
	require.LessOrEqual(t, len(results[0].Capture.Body()), max)
}

func TestAllResourcesPerSlot(t *testing.T) {
	const n = 4
	seen := make([]*request.Resources, n)
	var mu sync.Mutex
	targets := make(pool.Targets, n)
	for i := range n {
		idx := i
		targets[i] = mkTarget("x", func(_ http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[idx] = request.GetResources(r)
			mu.Unlock()
		})
	}
	created := make([]*request.Resources, n)
	cfg := Config{
		Mechanism: "test",
		Resources: func(idx int) *request.Resources {
			rsc := &request.Resources{}
			created[idx] = rsc
			return rsc
		},
	}
	parent := newParentGET(t)
	_, _ = All(context.Background(), parent, targets, cfg)

	for i := range n {
		require.NotNil(t, seen[i])
		require.Same(t, created[i], seen[i], "slot %d got a shared/foreign Resources", i)
		for j := range n {
			if i == j {
				continue
			}
			require.NotSame(t, seen[i], seen[j], "slots %d and %d share Resources", i, j)
		}
	}
}

func TestAllContextTransform(t *testing.T) {
	type ctxK struct{}
	targets := pool.Targets{
		mkTarget("ctx", func(_ http.ResponseWriter, r *http.Request) {
			v, _ := r.Context().Value(ctxK{}).(string)
			require.Equal(t, "wrapped", v)
		}),
	}
	cfg := Config{
		Mechanism: "test",
		Context: func(parent context.Context) context.Context {
			return context.WithValue(parent, ctxK{}, "wrapped")
		},
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, cfg)
	require.Len(t, results, 1)
	require.False(t, results[0].Failed)
	v, _ := results[0].Request.Context().Value(ctxK{}).(string)
	require.Equal(t, "wrapped", v)
}

func TestAllOnResultRunsInGoroutine(t *testing.T) {
	const n = 6
	var calls atomic.Int32
	targets := make(pool.Targets, n)
	for i := range n {
		body := fmt.Sprintf("b-%d", i)
		targets[i] = mkTarget(body, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
	}
	cfg := Config{
		Mechanism: "test",
		OnResult: func(idx int, r *Result) {
			require.Equal(t, idx, r.Index)
			require.NotNil(t, r.Request)
			require.NotNil(t, r.Capture)
			calls.Add(1)
		},
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, cfg)
	require.Len(t, results, n)
	require.Equal(t, int32(n), calls.Load())
}

func TestAllOnResultThreadSafety(t *testing.T) {
	const n = 50
	var mu sync.Mutex
	got := make([]int, 0, n)
	targets := make(pool.Targets, n)
	for i := range n {
		targets[i] = mkTarget("x", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	cfg := Config{
		Mechanism: "test",
		OnResult: func(idx int, _ *Result) {
			mu.Lock()
			got = append(got, idx)
			mu.Unlock()
		},
	}
	parent := newParentGET(t)
	_, _ = All(context.Background(), parent, targets, cfg)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, n)
	seen := make(map[int]bool, n)
	for _, idx := range got {
		require.False(t, seen[idx], "duplicate idx %d", idx)
		seen[idx] = true
	}
}

func TestAllNilTarget(t *testing.T) {
	targets := pool.Targets{
		mkTarget("ok-0", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok-0"))
		}),
		nil,
		mkTarget("ok-2", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok-2"))
		}),
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, 3)
	require.False(t, results[0].Failed)
	require.Equal(t, "ok-0", string(results[0].Capture.Body()))
	require.True(t, results[1].Failed)
	require.Nil(t, results[1].Capture)
	require.False(t, results[2].Failed)
	require.Equal(t, "ok-2", string(results[2].Capture.Body()))
}

func TestPrimeBodyForGET(t *testing.T) {
	parent := newParentGET(t)
	out, err := PrimeBody(parent)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, parent.Method, out.Method)
	require.Equal(t, parent.URL.String(), out.URL.String())
	require.NotNil(t, request.GetResources(out))
}

func TestPrimeBodyForPOST(t *testing.T) {
	const body = `{"q":"up"}`
	parent := newParentPOST(t, body)
	out, err := PrimeBody(parent)
	require.NoError(t, err)
	require.NotNil(t, out)
	rsc := request.GetResources(out)
	require.NotNil(t, rsc)
	require.Equal(t, body, string(rsc.RequestBody))

	out2, err := PrimeBody(out)
	require.NoError(t, err)
	require.Same(t, rsc, request.GetResources(out2))

	for range 3 {
		b, err := io.ReadAll(out2.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(b))
		_ = out2.Body.Close()
		out2.Body = io.NopCloser(bytes.NewReader(rsc.RequestBody))
	}
}

func TestPrimeBodyConcurrentClonesAreRaceFree(t *testing.T) {
	const body = `{"query":"sum(rate(metric[5m]))","start":"2024-01-01T00:00:00Z","end":"2024-01-01T01:00:00Z","step":"15s"}`
	parent := newParentPOST(t, body)
	primed, err := PrimeBody(parent)
	require.NoError(t, err)

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		idx := i
		wg.Go(func() {
			r2, _, err := PrepareClone(context.Background(), primed, idx, Config{Mechanism: "test"})
			if err != nil {
				errs <- err
				return
			}
			b, err := io.ReadAll(r2.Body)
			if err != nil {
				errs <- err
				return
			}
			if string(b) != body {
				errs <- fmt.Errorf("caller %d got %d bytes want %d", idx, len(b), len(body))
				return
			}
			errs <- nil
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
}

func TestPrepareCloneRespectsMaxBytes(t *testing.T) {
	const max = 64
	parent := newParentGET(t)
	r2, crw, err := PrepareClone(context.Background(), parent, 0, Config{Mechanism: "test", MaxCaptureBytes: max})
	require.NoError(t, err)
	require.NotNil(t, r2)
	require.NotNil(t, crw)

	n, err := crw.Write(bytes.Repeat([]byte("a"), max*4))
	require.NoError(t, err)
	require.Equal(t, max*4, n)
	require.True(t, crw.Truncated())
	require.LessOrEqual(t, len(crw.Body()), max)
}

func TestPrepareCloneNilResources(t *testing.T) {
	parent := newParentGET(t)
	cfg := Config{
		Mechanism: "test",
		Resources: func(_ int) *request.Resources { return nil },
	}
	r2, _, err := PrepareClone(context.Background(), parent, 0, cfg)
	require.NoError(t, err)
	require.Nil(t, request.GetResources(r2))
}

func TestAllNoLeaksOnNormalCompletion(t *testing.T) {
	const n = 4
	targets := make(pool.Targets, n)
	for i := range n {
		targets[i] = mkTarget("x", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
	}
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, n)
}

func TestAllEmptyTargets(t *testing.T) {
	parent := newParentGET(t)
	results, _ := All(context.Background(), parent, pool.Targets{}, Config{Mechanism: "test"})
	require.Empty(t, results)
}

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, fmt.Errorf("read failed") }

func TestAllCloneErrorSurfaces(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://trickstercache.org/", errReader{})
	require.NoError(t, err)
	targets := pool.Targets{
		mkTarget("unreached", func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("handler should not run when clone fails")
			w.WriteHeader(http.StatusOK)
		}),
	}
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, 1)
	require.True(t, results[0].Failed)
	require.Error(t, results[0].Err)
}

func TestPrimeBodyReadError(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://trickstercache.org/", errReader{})
	require.NoError(t, err)
	_, perr := PrimeBody(parent)
	require.Error(t, perr)
}

func TestPrepareCloneError(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://trickstercache.org/", errReader{})
	require.NoError(t, err)
	_, _, perr := PrepareClone(context.Background(), parent, 0, Config{Mechanism: "test"})
	require.Error(t, perr)
}

func TestAllNoLeaksOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	const n = 3
	started := make(chan struct{}, n)
	targets := make(pool.Targets, n)
	for i := range n {
		targets[i] = mkTarget("block", func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-r.Context().Done()
			w.WriteHeader(http.StatusGatewayTimeout)
		})
	}
	parent := newParentGET(t)
	done := make(chan struct{})
	go func() {
		_, _ = All(ctx, parent, targets, Config{Mechanism: "test"})
		close(done)
	}()
	for range n {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("handler never started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("All did not return after ctx cancel")
	}
}
