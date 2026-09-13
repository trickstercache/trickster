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
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func shopLoad(workers int) *loadLoop {
	return startLoad(workers, func() (*http.Response, error) {
		req, err := hostRequest(gatewayHTTPAddr, "shop.example.com", "/", nil)
		if err != nil {
			return nil, err
		}
		return hostClient.Do(req)
	})
}

// rollingRestartUnderLoad rolls the gateway Deployment while the shop route is under load and
// a slow streaming response is in flight, and asserts that no client saw an error or a cut body
func rollingRestartUnderLoad(t *testing.T) {
	t.Helper()
	// a body dripped over eight seconds outlives the old pod's removal from the Service, so
	// it completes only if the pod drains its in-flight requests before closing
	const dripBytes = 8
	drip, err := hostRequest(gatewayHTTPAddr, "bin.example.com",
		fmt.Sprintf("/drip?duration=8&numbytes=%d&delay=0", dripBytes), nil)
	require.NoError(t, err)
	slow := &http.Client{Timeout: 45 * time.Second}
	dripped := make(chan error, 1)
	var dripLen int
	go func() {
		resp, err := slow.Do(drip)
		if err != nil {
			dripped <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		dripLen = len(body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("drip answered %d", resp.StatusCode)
		}
		dripped <- err
	}()
	time.Sleep(time.Second)

	load := shopLoad(4)
	kubectlKind(t, "", "rollout", "restart", "deployment/trickster-gateway")
	restarted := time.Since(load.failures.start)
	kubectlKind(t, "", "rollout", "status", "deployment/trickster-gateway", "--timeout=180s")
	rolledOut := time.Since(load.failures.start)
	// keep load flowing while the old pod finishes draining
	time.Sleep(5 * time.Second)
	requests, errors, failures := load.end()
	t.Logf("gateway rolling restart: requests=%d errors=%d (restart issued +%dms, rolled out +%dms); failures: %s",
		requests, errors, restarted.Milliseconds(), rolledOut.Milliseconds(), failures)
	require.Positive(t, requests)
	require.Zero(t, errors,
		"a rolling restart of the gateway must produce zero client errors; failures: %s", failures)
	select {
	case err := <-dripped:
		require.NoError(t, err, "the response in flight across the restart must complete")
		require.Equal(t, dripBytes, dripLen, "the response in flight across the restart was cut short")
	case <-time.After(time.Minute):
		t.Fatal("the response in flight across the restart never completed")
	}
}

func TestGatewayRollingUpgradeKind(t *testing.T) {
	skipUnlessKind(t)
	waitForTrickster(t, gatewayMetricsAddr)
	waitRoute(t, gatewayHTTPAddr, "shop.example.com", "/", http.StatusOK, 2*time.Minute)
	waitRoute(t, gatewayHTTPAddr, "bin.example.com", "/get", http.StatusOK, time.Minute)
	rollingRestartUnderLoad(t)
	// the replacement serves every route, not only the one under load
	waitForTrickster(t, gatewayMetricsAddr)
	waitRoute(t, gatewayHTTPAddr, "bin.example.com", "/get", http.StatusOK, time.Minute)
	resp, _ := hostGet(t, gatewayHTTPAddr, "old.example.com", "/")
	require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
}

func churnRoute(i int) string {
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: churn-%d
  namespace: %s
spec:
  parentRefs:
    - name: edge
      sectionName: http
  hostnames:
    - churn%d.example.com
  rules:
    - backendRefs:
        - name: webecho
          port: 80
`, i, kindNamespace, i)
}

// soakSample is what one scrape of the gateway says about the process
type soakSample struct {
	goroutines, fds, rss, reloads, reloadFailures, reconcileErrors float64
	hasFDs                                                         bool
}

// soakScrape takes one complete scrape, requires the process gauges, and sums the counters
// that carry labels; a counter the scrape does not expose has never been incremented
func soakScrape(metricsAddr string) (soakSample, error) {
	snap, err := scrapeMetrics(metricsAddr)
	if err != nil {
		return soakSample{}, err
	}
	var s soakSample
	var ok bool
	if s.goroutines, ok = snap.value("go_goroutines", ""); !ok {
		return s, fmt.Errorf("go_goroutines missing from the scrape")
	}
	if s.rss, ok = snap.value("process_resident_memory_bytes", ""); !ok {
		return s, fmt.Errorf("process_resident_memory_bytes missing from the scrape")
	}
	s.fds, s.hasFDs = snap.value("process_open_fds", "")
	s.reloads, _ = snap.sum("trickster_config_reload_attempts_total")
	s.reloadFailures, _ = snap.sum("trickster_config_reload_failures_total")
	s.reconcileErrors, _ = snap.sum("trickster_kgw_reconcile_errors_total")
	return s, nil
}

func TestGatewaySoakKind(t *testing.T) {
	skipUnlessKind(t)
	if os.Getenv("TRICKSTER_SOAK_TEST") != "1" {
		t.Skip("soak runs only with TRICKSTER_SOAK_TEST=1")
	}
	duration := 10 * time.Minute
	if d := os.Getenv("TRICKSTER_SOAK_DURATION"); d != "" {
		parsed, err := time.ParseDuration(d)
		require.NoError(t, err, "invalid TRICKSTER_SOAK_DURATION")
		duration = parsed
	}
	const (
		churnEvery  = 3 * time.Second
		rotateEvery = 60 * time.Second
		churnSlots  = 8
		secureHost  = "secure.example.com"
	)
	waitForTrickster(t, gatewayMetricsAddr)
	waitRoute(t, gatewayHTTPAddr, "shop.example.com", "/", http.StatusOK, 2*time.Minute)
	waitRoute(t, gatewayHTTPAddr, "bin.example.com", "/get", http.StatusOK, time.Minute)
	manifest, serial := tlsSecretManifest(t, "edge-tls", secureHost)
	applyKind(t, manifest)
	waitServedSerial(t, gatewayHTTPSAddr, secureHost, serial, 2*time.Minute)
	// the serial the most recent rotation installed
	current := serial

	stop := make(chan struct{})
	var wg sync.WaitGroup
	targets := [][3]string{
		{"shop.example.com", "/", ""},
		{"bin.example.com", "/cache/60?soak=1", ""},
		{"shop.example.com", "/api/orders", ""},
		{"shop.example.com", "/", "always"},
	}
	var i int
	var mtx sync.Mutex
	load := startLoad(4, func() (*http.Response, error) {
		mtx.Lock()
		target := targets[i%len(targets)]
		i++
		mtx.Unlock()
		var h http.Header
		if target[2] != "" {
			h = http.Header{"X-Canary": {target[2]}}
		}
		req, err := hostRequest(gatewayHTTPAddr, target[0], target[1], h)
		if err != nil {
			return nil, err
		}
		return hostClient.Do(req)
	})

	// a rotating window of churnSlots/2 routes is added and removed throughout
	var changes, rotations int
	wg.Go(func() {
		ticker := time.NewTicker(churnEvery)
		defer ticker.Stop()
		var n int
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			if _, err := kubectlTry(churnRoute(n%churnSlots), "apply", "-f", "-"); err != nil {
				t.Error(err)
			}
			if _, err := kubectlTry(churnRoute((n+churnSlots/2)%churnSlots),
				"delete", "--ignore-not-found", "--wait=false", "-f", "-"); err != nil {
				t.Error(err)
			}
			mtx.Lock()
			changes += 2
			mtx.Unlock()
			// a route added two ticks ago is being served by now
			if n >= 2 {
				host := fmt.Sprintf("churn%d.example.com", (n-2)%churnSlots)
				req, err := hostRequest(gatewayHTTPAddr, host, "/", nil)
				if err != nil {
					t.Error(err)
					continue
				}
				resp, err := hostClient.Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						t.Errorf("churn route %s answered %d", host, resp.StatusCode)
					}
				}
			}
			n++
		}
	})
	wg.Go(func() {
		ticker := time.NewTicker(rotateEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			manifest, serial := tlsSecretManifest(t, "edge-tls", secureHost)
			if _, err := kubectlTry(manifest, "apply", "-f", "-"); err != nil {
				t.Error(err)
				continue
			}
			if !rotatedWithin(serial, time.Minute) {
				t.Errorf("certificate %s was not served within a minute of the rotation", serial)
			}
			mtx.Lock()
			rotations++
			current = serial
			mtx.Unlock()
		}
	})

	// the first and last quarters' medians decide; every sample is one complete scrape
	var samples []soakSample
	var errorsByWindow []int64
	sampleEvery := max(duration/60, 5*time.Second)
	deadline := time.Now().Add(duration)
	time.Sleep(min(10*time.Second, duration/10))
	first, err := soakScrape(gatewayMetricsAddr)
	require.NoError(t, err)
	var lastErrors int64
	for time.Now().Before(deadline) {
		s, err := soakScrape(gatewayMetricsAddr)
		require.NoError(t, err, "a scrape failed during the soak")
		samples = append(samples, s)
		errors := load.errors.Load()
		errorsByWindow = append(errorsByWindow, errors-lastErrors)
		lastErrors = errors
		time.Sleep(sampleEvery)
	}
	close(stop)
	wg.Wait()
	requests, errors, failures := load.end()
	mtx.Lock()
	totalChanges, totalRotations := changes, rotations
	mtx.Unlock()
	require.GreaterOrEqual(t, len(samples), 8, "not enough samples for a verdict")

	median := func(vals []float64) float64 {
		sort.Float64s(vals)
		return vals[len(vals)/2]
	}
	quarter := len(samples) / 4
	var earlyG, lateG, earlyF, lateF, earlyR, lateR []float64
	for i, s := range samples {
		switch {
		case i < quarter:
			earlyG, earlyF, earlyR = append(earlyG, s.goroutines), append(earlyF, s.fds), append(earlyR, s.rss)
		case i >= len(samples)-quarter:
			lateG, lateF, lateR = append(lateG, s.goroutines), append(lateF, s.fds), append(lateR, s.rss)
		}
	}
	last := samples[len(samples)-1]
	gGrowth := median(lateG) - median(earlyG)
	fGrowth := median(lateF) - median(earlyF)
	rGrowth := median(lateR) - median(earlyR)
	reloads := last.reloads - first.reloads
	var worstWindow int64
	for _, n := range errorsByWindow {
		worstWindow = max(worstWindow, n)
	}
	t.Logf("gateway soak %s: requests=%d errors=%d (worst window %d) changes=%d rotations=%d reloads=%.0f; "+
		"goroutine growth=%.0f fd growth=%.0f rss growth=%.0f MiB; failures: %s",
		duration, requests, errors, worstWindow, totalChanges, totalRotations, reloads,
		gGrowth, fGrowth, rGrowth/(1024*1024), failures)

	require.LessOrEqual(t, gGrowth, 25.0, "goroutine count grew across the soak")
	if first.hasFDs {
		for _, s := range samples {
			require.True(t, s.hasFDs, "process_open_fds disappeared during the soak")
		}
		require.LessOrEqual(t, fGrowth, 15.0, "open file descriptors grew across the soak")
	}
	require.LessOrEqual(t, rGrowth, max(0.3*median(earlyR), 64*1024*1024),
		"resident memory grew across the soak")
	require.LessOrEqual(t, reloads, float64(totalChanges+totalRotations+2),
		"more reloads than changes: the debounce is not coalescing")
	require.Zero(t, last.reloadFailures-first.reloadFailures, "a reload failed during the soak")
	require.Zero(t, last.reconcileErrors-first.reconcileErrors, "a reconcile failed during the soak")
	require.LessOrEqual(t, worstWindow, int64(5), "a burst of errors within one sample window")
	require.LessOrEqual(t, float64(errors), 0.001*float64(requests),
		"more than 0.1%% of requests failed; failures: %s", failures)

	// the gateway is rolled under load at the end of the soak
	for n := range churnSlots {
		deleteKind(t, churnRoute(n))
	}
	rollingRestartUnderLoad(t)
	waitForTrickster(t, gatewayMetricsAddr)
	// the replacement installed the certificate the last rotation left in the Secret
	waitServedSerial(t, gatewayHTTPSAddr, secureHost, current, time.Minute)
}

// rotatedWithin polls until the HTTPS listener presents the serial; safe from a goroutine
func rotatedWithin(serial *big.Int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := servedSerial(gatewayHTTPSAddr, "secure.example.com"); got != nil && got.Cmp(serial) == 0 {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
