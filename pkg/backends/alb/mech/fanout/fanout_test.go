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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestAllOrderedResults(t *testing.T) {
	const n = 5
	targets := make(pool.Targets, n)
	for i := range n {
		body := fmt.Sprintf("slot-%d", i)
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
	}
	parent := albpool.NewParentGET(t)
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
		tg, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-r.Context().Done()
			w.WriteHeader(http.StatusGatewayTimeout)
		}))
		return tg
	}
	targets := pool.Targets{mk(), mk(), mk()}
	parent := albpool.NewParentGET(t)

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

func TestAllCancellationStopsQueuedDispatchAndResultProcessing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	firstStarted := make(chan struct{})
	var queuedStarted atomic.Int32
	var onResultCalls atomic.Int32

	first, _ := albpool.Target(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(firstStarted)
		<-r.Context().Done()
	}))
	queuedHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		queuedStarted.Add(1)
	})
	second, _ := albpool.Target(queuedHandler)
	third, _ := albpool.Target(queuedHandler)
	targets := pool.Targets{first, second, third}
	parent := albpool.NewParentGET(t)

	type outcome struct {
		results []Result
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := All(ctx, parent, targets, Config{
			Mechanism:        "test",
			ConcurrencyLimit: 1,
			OnResult: func(int, *Result) {
				onResultCalls.Add(1)
			},
		})
		done <- outcome{results: results, err: err}
	}()

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first handler never started")
	}
	cancel()

	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.Canceled)
		require.Len(t, got.results, len(targets))
		for i := range got.results {
			require.Equal(t, i, got.results[i].Index)
			require.True(t, got.results[i].Failed, "slot %d should be canceled", i)
			require.ErrorIs(t, got.results[i].Err, context.Canceled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("All did not return after context cancellation")
	}

	require.Zero(t, queuedStarted.Load(), "queued handlers started after cancellation")
	require.Zero(t, onResultCalls.Load(), "OnResult ran after cancellation")
}

func TestAllPanicRecovered(t *testing.T) {
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok-0"))
	}))
	t1, _ := albpool.Target(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	t2, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok-2"))
	}))
	targets := pool.Targets{t0, t1, t2}
	parent := albpool.NewParentGET(t)
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
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		}))
	}
	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test", ConcurrencyLimit: limit})
	require.Len(t, results, n)
	require.LessOrEqual(t, int(maxSeen.Load()), limit, "max in-flight exceeded limit")
	require.Greater(t, int(maxSeen.Load()), 0, "no concurrency observed")
}

func TestAllCaptureBound(t *testing.T) {
	const max = 1024
	big := bytes.Repeat([]byte("a"), 100*1024)
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(big)
	}))
	targets := pool.Targets{t0}
	parent := albpool.NewParentGET(t)
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
		targets[i], _ = albpool.Target(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[idx] = request.GetResources(r)
			mu.Unlock()
		}))
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
	parent := albpool.NewParentGET(t)
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
	var seenInHandler atomic.Value
	t0, _ := albpool.Target(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		v, _ := r.Context().Value(ctxK{}).(string)
		seenInHandler.Store(v)
	}))
	targets := pool.Targets{t0}
	cfg := Config{
		Mechanism: "test",
		Context: func(parent context.Context) context.Context {
			return context.WithValue(parent, ctxK{}, "wrapped")
		},
	}
	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, targets, cfg)
	require.Len(t, results, 1)
	require.False(t, results[0].Failed)
	require.Equal(t, "wrapped", seenInHandler.Load())
	v, _ := results[0].Request.Context().Value(ctxK{}).(string)
	require.Equal(t, "wrapped", v)
}

func TestAllOnResultRunsInGoroutine(t *testing.T) {
	const n = 6
	var calls atomic.Int32
	targets := make(pool.Targets, n)
	for i := range n {
		body := fmt.Sprintf("b-%d", i)
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
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
	parent := albpool.NewParentGET(t)
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
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}
	cfg := Config{
		Mechanism: "test",
		OnResult: func(idx int, _ *Result) {
			mu.Lock()
			got = append(got, idx)
			mu.Unlock()
		},
	}
	parent := albpool.NewParentGET(t)
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
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok-0"))
	}))
	t2, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok-2"))
	}))
	targets := pool.Targets{t0, nil, t2}
	parent := albpool.NewParentGET(t)
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
	parent := albpool.NewParentGET(t)
	out, err := PrimeBody(parent)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, parent.Method, out.Method)
	require.Equal(t, parent.URL.String(), out.URL.String())
	require.NotNil(t, request.GetResources(out))
}

func TestPrimeBodyForPOST(t *testing.T) {
	const body = `{"q":"up"}`
	parent := albpool.NewParentPOST(t, strings.NewReader(body))
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
	parent := albpool.NewParentPOST(t, strings.NewReader(body))
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
	parent := albpool.NewParentGET(t)
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
	parent := albpool.NewParentGET(t)
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
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
	}
	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, n)
}

func TestAllEmptyTargets(t *testing.T) {
	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, pool.Targets{}, Config{Mechanism: "test"})
	require.Empty(t, results)
}

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, fmt.Errorf("read failed") }

func TestAllCloneErrorSurfaces(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://"+appinfo.Domain+"/", errReader{})
	require.NoError(t, err)
	t0, _ := albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not run when clone fails")
		w.WriteHeader(http.StatusOK)
	}))
	targets := pool.Targets{t0}
	results, _ := All(context.Background(), parent, targets, Config{Mechanism: "test"})
	require.Len(t, results, 1)
	require.True(t, results[0].Failed)
	require.Error(t, results[0].Err)
}

func TestPrimeBodyReadError(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://"+appinfo.Domain+"/", errReader{})
	require.NoError(t, err)
	_, perr := PrimeBody(parent)
	require.Error(t, perr)
}

func TestPrepareCloneError(t *testing.T) {
	parent, err := http.NewRequest(http.MethodPost, "http://"+appinfo.Domain+"/", errReader{})
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
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-r.Context().Done()
			w.WriteHeader(http.StatusGatewayTimeout)
		}))
	}
	parent := albpool.NewParentGET(t)
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

func TestScatterReleasesUnwantedCaptures(t *testing.T) {
	// a slot whose result no caller will be given has its capture released once it is done with it,
	// and the others keep theirs
	targets := make(pool.Targets, 3)
	for i := range targets {
		body := fmt.Sprintf("slot-%d", i)
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Slot", body)
			_, _ = w.Write([]byte(body))
		}))
	}
	var unwanted atomic.Pointer[capture.CaptureResponseWriter]
	cfg := Config{Mechanism: "test", OnResult: func(i int, r *Result) {
		if i == 1 {
			// the result is whole when OnResult is given it, and released after
			require.Equal(t, "slot-1", string(r.Capture.Body()))
			unwanted.Store(r.Capture)
		}
	}}
	results, err := scatter(context.Background(), albpool.NewParentGET(t), targets, cfg, func(i int, r *Result) {
		r.unwanted = i == 1
	})
	require.NoError(t, err)
	require.Nil(t, results[1].Capture)
	require.Empty(t, unwanted.Load().Body(), "the unwanted capture was not released")
	require.Empty(t, unwanted.Load().Header())
	for _, i := range []int{0, 2} {
		require.Equal(t, fmt.Sprintf("slot-%d", i), string(results[i].Capture.Body()))
	}
	ReleaseCaptures(results)
	for i := range results {
		require.Nil(t, results[i].Capture)
	}
}

type releaseCounter struct {
	mu     sync.Mutex
	counts map[*capture.CaptureResponseWriter]int
}

func countReleases(t *testing.T) *releaseCounter {
	rc := &releaseCounter{counts: map[*capture.CaptureResponseWriter]int{}}
	// the hook is restored only once the goroutines a failed test left behind have drained
	before := goleak.IgnoreCurrent()
	prev := releaseCapture
	releaseCapture = func(c *capture.CaptureResponseWriter) {
		rc.mu.Lock()
		rc.counts[c]++
		rc.mu.Unlock()
		prev(c)
	}
	t.Cleanup(func() {
		_ = goleak.Find(before)
		releaseCapture = prev
	})
	return rc
}

func (rc *releaseCounter) of(c *capture.CaptureResponseWriter) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.counts[c]
}

func (rc *releaseCounter) total() (n int, each bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	each = true
	for _, c := range rc.counts {
		n += c
		each = each && c == 1
	}
	return n, each
}

func (rc *releaseCounter) requireEventually(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { got, _ := rc.total(); return got >= n }, 2*time.Second, time.Millisecond)
	got, each := rc.total()
	require.Equal(t, n, got)
	require.True(t, each, "a capture was released more than once")
}

type slotCaptures struct {
	mu   sync.Mutex
	byID map[int]*capture.CaptureResponseWriter
}

// records each slot's capture, which must not yet be released when a callback is given it
func (sc *slotCaptures) onResult(t *testing.T, rc *releaseCounter) func(int, *Result) {
	return func(i int, r *Result) {
		if r.Capture == nil {
			return
		}
		if rc.of(r.Capture) != 0 {
			t.Errorf("slot %d's capture was released before its callback", i)
		}
		sc.mu.Lock()
		sc.byID[i] = r.Capture
		sc.mu.Unlock()
	}
}

func TestWaitForFirstReleasesWhatTheCallerIsNotGiven(t *testing.T) {
	good := func(r *Result) bool { return r.Capture.StatusCode() < 400 }
	// the winner waits until the failed slot's callback has run and the canceled slot has started,
	// which reach it in either order
	for _, callbackFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("a failed slot and a canceled one, callback first %t", callbackFirst), func(t *testing.T) {
			rc := countReleases(t)
			recorded, started := make(chan struct{}), make(chan struct{})
			var callbacks, starts atomic.Int32
			targets := make(pool.Targets, 3)
			targets[0], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !callbackFirst {
					<-started
				}
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("failed"))
			}))
			targets[1], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				<-recorded
				<-started
				_, _ = w.Write([]byte("winner"))
			}))
			targets[2], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if callbackFirst {
					<-recorded
				}
				starts.Add(1)
				close(started)
				<-r.Context().Done()
				_, _ = w.Write([]byte("loser"))
			}))
			sc := &slotCaptures{byID: map[int]*capture.CaptureResponseWriter{}}
			record := sc.onResult(t, rc)
			cfg := Config{Mechanism: "test", OnResult: func(i int, r *Result) {
				record(i, r)
				if i == 0 {
					callbacks.Add(1)
					close(recorded)
				}
			}}
			winner, results, err := WaitForFirst(context.Background(), albpool.NewParentGET(t), targets, cfg, good)
			require.NoError(t, err)
			require.Equal(t, 1, winner)
			kept := results[winner].Capture
			require.Equal(t, "winner", string(kept.Body()))
			require.Equal(t, int32(1), callbacks.Load(), "the failed slot's callback")
			require.Equal(t, int32(1), starts.Load(), "the canceled slot's start")
			// the canceled slot is released as it returns, and the failed one once every slot has
			rc.requireEventually(t, 2)
			sc.mu.Lock()
			failed := sc.byID[0]
			sc.mu.Unlock()
			require.NotNil(t, failed)
			require.Equal(t, 1, rc.of(failed))
			require.Zero(t, rc.of(kept), "the winner is the caller's to release")
			ReleaseCaptures(results)
			rc.requireEventually(t, 3)
			rc.mu.Lock()
			require.Len(t, rc.counts, 3, "a capture per slot")
			rc.mu.Unlock()
		})
	}
	t.Run("parent cancellation", func(t *testing.T) {
		rc := countReleases(t)
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{}, 3)
		targets := make(pool.Targets, 3)
		for i := range targets {
			targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-r.Context().Done()
				_, _ = w.Write([]byte("late"))
			}))
		}
		go func() {
			for range targets {
				<-started
			}
			cancel()
		}()
		winner, results, err := WaitForFirst(ctx, albpool.NewParentGET(t), targets, Config{Mechanism: "test"}, good)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, -1, winner)
		ReleaseCaptures(results)
		rc.requireEventually(t, 3)
	})
	t.Run("no winner", func(t *testing.T) {
		rc := countReleases(t)
		targets := make(pool.Targets, 3)
		for i := range targets {
			targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
		}
		sc := &slotCaptures{byID: map[int]*capture.CaptureResponseWriter{}}
		cfg := Config{Mechanism: "test", OnResult: sc.onResult(t, rc)}
		winner, results, err := WaitForFirst(context.Background(), albpool.NewParentGET(t), targets, cfg, good)
		require.NoError(t, err)
		require.Equal(t, -1, winner)
		// the gathered results are the caller's fallback, so nothing is released for it
		n, _ := rc.total()
		require.Zero(t, n)
		for i := range results {
			require.Equal(t, http.StatusServiceUnavailable, results[i].Capture.StatusCode())
		}
		ReleaseCaptures(results)
		rc.requireEventually(t, 3)
	})
}

func BenchmarkWaitForFirstReleasingCaptures(b *testing.B) {
	// WaitForFirst over members that each answer 64 KiB, three failing before the good one, with the
	// caller done with the winner as FGR is
	body := bytes.Repeat([]byte("trickster "), 64<<10/10)
	targets := make(pool.Targets, 4)
	var failed sync.WaitGroup
	for i := range targets {
		code := http.StatusServiceUnavailable
		if i == len(targets)-1 {
			code = http.StatusOK
		}
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if code == http.StatusOK {
				failed.Wait()
			} else {
				defer failed.Done()
			}
			w.Header().Set(headers.NameContentLength, strconv.Itoa(len(body)))
			w.WriteHeader(code)
			_, _ = w.Write(body)
		}))
	}
	parent := albpool.NewParentGET(b)
	good := func(r *Result) bool { return r.Capture.StatusCode() < 400 }
	b.ReportAllocs()
	for b.Loop() {
		failed.Add(len(targets) - 1)
		_, results, _ := WaitForFirst(context.Background(), parent, targets, Config{Mechanism: "bench"}, good)
		ReleaseCaptures(results)
	}
}

func BenchmarkAllReleasingCaptures(b *testing.B) {
	// All over members that each answer 64 KiB, with the caller done with the captures as NLM is
	body := bytes.Repeat([]byte("trickster "), 64<<10/10)
	targets := make(pool.Targets, 4)
	for i := range targets {
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameContentLength, strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		}))
	}
	parent := albpool.NewParentGET(b)
	b.ReportAllocs()
	for b.Loop() {
		results, _ := All(context.Background(), parent, targets, Config{Mechanism: "bench"})
		ReleaseCaptures(results)
	}
}
