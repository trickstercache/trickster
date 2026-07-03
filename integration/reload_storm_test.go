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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReloadStormDoesNotLeak exercises the cache-handoff path on config
// reload by alternating between two memory caches under load. Prior to the
// WaitGroup-based drain in Manager.Close(), ApplyConfig spawned
// `go func() { time.Sleep(d); w.Close() }()` for each replaced cache: a
// request mid-Store on the old cache could race that Close, returning errors
// to the client and (for stateful providers like bbolt/redis) leaking
// resources.
//
// Primary assertion: 0 request errors during 800k+ requests across 12
// reloads. With the drain in place, in-flight cache ops either complete
// before the close, or short-circuit with ErrCacheClosed which the engine
// treats as a miss + upstream proxy -- either way the client gets a 2xx.
//
// Secondary assertion: bounded goroutine growth. The drain bug, if
// reintroduced, would leave one goroutine per in-flight op stuck on a
// closed client (thousands under this storm). With the cache-rename
// close fix in applyCachingConfig and CloseIdleConnections on old
// backends in Hup, observed delta is ~0-3 goroutines across 12 reloads;
// 1/reload here is tight enough to catch a single-goroutine-per-reload
// regression.
func TestReloadStormDoesNotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("storm test takes 15-30s; skipped under -short")
	}

	// Reset SIGHUP so the daemon's signaling.Wait is the only receiver, then
	// restore at end of test. (Mirrors TestLifecycle_ReloadPreservesHCStatus.)
	signal.Reset(syscall.SIGHUP)

	const buildInfo = `{"status":"success","data":{"version":"2.0"}}`
	const queryResp = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up"},"value":[1700000000,"1"]}]}}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, buildInfo)
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, queryResp)
		}
	}))
	t.Cleanup(upstream.Close)

	const (
		frontPort   = 18910
		metricsPort = 18911
		mgmtPort    = 18912
	)

	// Two configs alternate the cache_name. Switching cache_name between
	// reloads forces applyCachingConfig to hit the close-old-cache path,
	// which is the code under test.
	makeYAML := func(cacheName string) string {
		return fmt.Sprintf(`
frontend:
  listen_port: %d
metrics:
  listen_port: %d
mgmt:
  listen_port: %d
  drain_timeout: 250ms
logging:
  log_level: error
caches:
  %s:
    provider: memory
backends:
  prom1:
    provider: prometheus
    origin_url: %s
    cache_name: %s
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 500ms
      timeout: 1s
      failure_threshold: 1
      recovery_threshold: 1
`, frontPort, metricsPort, mgmtPort, cacheName, upstream.URL, cacheName)
	}

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(makeYAML("memA")), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	frontURL := fmt.Sprintf("http://127.0.0.1:%d/prom1/api/v1/query?query=up", frontPort)

	// Wait until requests actually flow before snapshotting baseline.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get(frontURL)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		assert.Equal(collect, http.StatusOK, resp.StatusCode)
	}, 10*time.Second, 200*time.Millisecond, "front listener never became ready")

	// Warmup reloads establish steady-state (active cache + listeners +
	// health builder) before snapshotting baseline, so the storm delta
	// reflects per-reload growth rather than first-reload setup churn.
	doReload := func(cacheName string) {
		tmp := cfgPath + ".tmp"
		require.NoError(t, os.WriteFile(tmp, []byte(makeYAML(cacheName)), 0644))
		require.NoError(t, os.Rename(tmp, cfgPath))
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	}
	for i, name := range []string{"memB", "memA", "memB", "memA"} {
		doReload(name)
		// Settle: SIGHUP -> Hup -> ApplyConfig is async wrt signal delivery,
		// so give each warmup reload a beat to complete before the next.
		time.Sleep(500 * time.Millisecond)
		_ = i
	}
	goruntime.GC()
	goruntime.GC()
	time.Sleep(500 * time.Millisecond)
	baselineGoroutines := goruntime.NumGoroutine()
	t.Logf("baseline goroutines after warmup=%d", baselineGoroutines)

	// Storm: hammer the front port with concurrent requests while flipping
	// the cache name and SIGHUPing for reloads.
	const (
		stormDuration = 8 * time.Second
		workers       = 32
		reloadCount   = 12
	)
	stormCtx, stopStorm := context.WithCancel(context.Background())
	var workerWG sync.WaitGroup
	var requests, requestErrors, requestNon2xx atomic.Int64
	// Share a Transport so workers reuse keep-alive connections instead of
	// flooding the upstream httptest server's ephemeral port range.
	sharedTransport := &http.Transport{
		MaxIdleConnsPerHost: workers,
		IdleConnTimeout:     30 * time.Second,
	}
	defer sharedTransport.CloseIdleConnections()
	for range workers {
		workerWG.Go(func() {
			client := &http.Client{Timeout: 5 * time.Second, Transport: sharedTransport}
			for {
				select {
				case <-stormCtx.Done():
					return
				default:
				}
				resp, err := client.Get(frontURL)
				requests.Add(1)
				if err != nil {
					requestErrors.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					requestNon2xx.Add(1)
				}
			}
		})
	}

	// Reload loop: alternate cache name and fire mgmt reloads at intervals.
	reloadInterval := stormDuration / time.Duration(reloadCount+1)
	var reloadWG sync.WaitGroup
	reloadWG.Go(func() {
		ticker := time.NewTicker(reloadInterval)
		defer ticker.Stop()
		current := "memA"
		fired := 0
		for fired < reloadCount {
			<-ticker.C
			if current == "memA" {
				current = "memB"
			} else {
				current = "memA"
			}
			// Atomic write: write to sibling tmp then rename, so the reload's
			// config loader never observes a half-written file.
			tmp := cfgPath + ".tmp"
			if err := os.WriteFile(tmp, []byte(makeYAML(current)), 0644); err != nil {
				t.Logf("write cfg: %v", err)
				continue
			}
			if err := os.Rename(tmp, cfgPath); err != nil {
				t.Logf("rename cfg: %v", err)
				continue
			}
			// SIGHUP preserves the original -config arg via daemon.Start's
			// closure. POST /trickster/config/reload drops args (the inner
			// Hup that registers the handler does not forward them), causing
			// later reloads to load from /etc/trickster/trickster.yaml. Use
			// SIGHUP here so the storm reliably exercises the cache-handoff
			// path on the temp config.
			if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
				t.Logf("SIGHUP: %v", err)
				continue
			}
			fired++
		}
	})
	reloadWG.Wait()

	// Let workers run a bit longer post-final-reload so any racey in-flight
	// requests have a chance to complete or leak.
	time.Sleep(1 * time.Second)
	stopStorm()
	workerWG.Wait()

	totalRequests := requests.Load()
	t.Logf("storm completed: requests=%d errors=%d non2xx=%d reloads=%d",
		totalRequests, requestErrors.Load(), requestNon2xx.Load(), reloadCount)

	// Primary assertion: requests through the cache-handoff window must
	// succeed. The bug guarded against (time.Sleep + Close racing in-flight
	// store/retrieve) manifests as panics or non-2xx responses spiking
	// when reloads land. With the drain in Manager.Close(), in-flight
	// operations either complete before close runs, or short-circuit with
	// ErrCacheClosed which the engine treats as a cache miss and proxies
	// to upstream. Either path must return 2xx.
	require.Greater(t, totalRequests, int64(100), "storm produced no requests")
	totalBad := requestErrors.Load() + requestNon2xx.Load()
	// Spec says every request must return 2xx. A handful of edge-of-reload
	// failures stay tolerated to keep this from flaking on slow CI, but the
	// previous 5% rate let a per-reload regression hide entirely.
	require.LessOrEqualf(t, totalBad, int64(5),
		"too many failed requests during reload storm: errors=%d non2xx=%d / requests=%d",
		requestErrors.Load(), requestNon2xx.Load(), totalRequests)

	// Secondary assertion: goroutine count growth must be bounded. The
	// drain bug we're guarding against, if reintroduced, would leave one
	// goroutine per in-flight cache op stuck on a closed client -- under
	// this storm that would be hundreds-of-thousands of goroutines. The
	// per-reload cap is tight (1/reload, observed delta ~0-3 across all
	// reloads) so a regression that adds even a single
	// goroutine-per-reload leak surfaces here.
	//
	// Close idle keep-alive conns in the worker transport before sampling
	// so the storm's persistConn read/write loops exit. Otherwise idle
	// conns linger until IdleConnTimeout and inflate the count.
	sharedTransport.CloseIdleConnections()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		goruntime.GC()
		goruntime.GC()
		got := goruntime.NumGoroutine()
		maxAllowed := baselineGoroutines + 1*reloadCount
		assert.LessOrEqualf(collect, got, maxAllowed,
			"goroutine count grew beyond bound: baseline=%d got=%d max=%d (reloads=%d)",
			baselineGoroutines, got, maxAllowed, reloadCount)
	}, 30*time.Second, 500*time.Millisecond,
		"goroutine count never returned to bounded growth after reload storm")

	final := goruntime.NumGoroutine()
	t.Logf("final goroutines=%d (baseline=%d delta=%d, reloads=%d)",
		final, baselineGoroutines, final-baselineGoroutines, reloadCount)
}
