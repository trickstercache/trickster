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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealthPageAfterReload exercises the /trickster/health endpoint across
// a config reload (POST mgmt /trickster/config/reload) and asserts the page
// reflects a status flip on the upstream backend that occurs *after* the
// reload completes.
//
// The hypothesis under test: the StatusHandler builder goroutine subscribes
// to the original healthchecker; on reload Shutdown() fires the closer and
// the builder exits. If the new mgmt router's StatusHandler isn't wired up
// to the new healthchecker's statuses, the page freezes.
func TestHealthPageAfterReload(t *testing.T) {
	const buildInfo = `{"status":"success","data":{"version":"2.0"}}`
	const queryResp = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up"},"value":[1700000000,"1"]}]}}`

	var hcShouldFail atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			if hcShouldFail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, buildInfo)
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, queryResp)
		}
	}))
	t.Cleanup(upstream.Close)

	const (
		frontPort   = 18900
		metricsPort = 18901
		mgmtPort    = 18902
	)

	yaml := fmt.Sprintf(`
frontend:
  listen_port: %d
metrics:
  listen_port: %d
mgmt:
  listen_port: %d
logging:
  log_level: error
caches:
  mem1:
    provider: memory
backends:
  prom1:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  alb-fr-test:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - prom1
`, frontPort, metricsPort, mgmtPort, upstream.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	healthURL := "http://" + metricsAddr + "/trickster/health"

	// Wait until prom1 reaches available before the reload.
	requireHealthState(t, healthURL, "prom1", "available", 10*time.Second)

	// Trigger reload via the mgmt port. Rewrite the config file first so
	// the daemon detects a non-stale change (logging level toggle is enough).
	yaml2 := fmt.Sprintf(`
frontend:
  listen_port: %d
metrics:
  listen_port: %d
mgmt:
  listen_port: %d
logging:
  log_level: warn
caches:
  mem1:
    provider: memory
backends:
  prom1:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  alb-fr-test:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - prom1
`, frontPort, metricsPort, mgmtPort, upstream.URL)
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml2), 0644))

	reloadURL := fmt.Sprintf("http://127.0.0.1:%d/trickster/config/reload", mgmtPort)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get(reloadURL)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(collect, http.StatusOK, resp.StatusCode,
			"reload status %d body=%s", resp.StatusCode, string(body))
	}, 5*time.Second, 250*time.Millisecond, "reload endpoint never accepted request")

	// Give the reload a beat to settle (drain timeout, new HC start).
	time.Sleep(500 * time.Millisecond)

	// Confirm prom1 is still observable on the new health page.
	requireHealthState(t, healthURL, "prom1", "available", 10*time.Second)

	// Flip the upstream so its healthcheck starts failing. With
	// failure_threshold=1 and interval=100ms the new HC should mark
	// prom1 Failing within a few hundred milliseconds.
	hcShouldFail.Store(true)

	// The /health page must reflect the new status. If the new mgmt
	// router's builder is not subscribed to the new statuses, this
	// assertion fails -- the page stays stuck on "available".
	requireHealthState(t, healthURL, "prom1", "unavailable", 10*time.Second)

	// The ALB membership view must also reflect the post-reload status.
	// With its sole pool member failing, alb-fr-test should land in the
	// unavailable bucket with prom1 listed as an unavailable member.
	requireALBMemberState(t, healthURL, "alb-fr-test", "prom1", "unavailable", 10*time.Second)
}

// requireALBMemberState polls the /trickster/health JSON until the named
// ALB lists the named pool member in the requested membership bucket
// ("available" or "unavailable").
func requireALBMemberState(t *testing.T, healthURL, alb, member, bucket string, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := http.NewRequest(http.MethodGet, healthURL, nil)
		if !assert.NoError(collect, err) {
			return
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		type entry struct {
			Name                   string   `json:"name"`
			AvailablePoolMembers   []string `json:"availablePoolMembers"`
			UnavailablePoolMembers []string `json:"unavailablePoolMembers"`
		}
		var hs struct {
			Available   []entry `json:"available"`
			Unavailable []entry `json:"unavailable"`
		}
		if !assert.NoError(collect, json.Unmarshal(b, &hs),
			"health payload was not JSON: %s", string(b)) {
			return
		}
		all := append([]entry{}, hs.Available...)
		all = append(all, hs.Unavailable...)
		var got entry
		var found bool
		for _, e := range all {
			if e.Name == alb {
				got = e
				found = true
				break
			}
		}
		if !assert.True(collect, found, "ALB %q not present in health page (body=%s)", alb, string(b)) {
			return
		}
		var pool []string
		switch bucket {
		case "available":
			pool = got.AvailablePoolMembers
		case "unavailable":
			pool = got.UnavailablePoolMembers
		default:
			collect.Errorf("unknown bucket %q", bucket)
			return
		}
		assert.Contains(collect, pool, member,
			"expected member %q in ALB %q %sPoolMembers=%v (body=%s)",
			member, alb, bucket, pool, string(b))
	}, timeout, 250*time.Millisecond,
		"ALB %q never listed %q in %sPoolMembers at %s", alb, member, bucket, healthURL)
}

// requireHealthState polls the /trickster/health JSON until the named
// backend appears in the requested bucket ("available" or "unavailable"),
// or fails the test after timeout.
func requireHealthState(t *testing.T, healthURL, name, bucket string, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := http.NewRequest(http.MethodGet, healthURL, nil)
		if !assert.NoError(collect, err) {
			return
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		var hs struct {
			Available []struct {
				Name string `json:"name"`
			} `json:"available"`
			Unavailable []struct {
				Name string `json:"name"`
			} `json:"unavailable"`
		}
		if !assert.NoError(collect, json.Unmarshal(b, &hs),
			"health payload was not JSON: %s", string(b)) {
			return
		}
		var names []string
		switch bucket {
		case "available":
			for _, a := range hs.Available {
				names = append(names, a.Name)
			}
		case "unavailable":
			for _, a := range hs.Unavailable {
				names = append(names, a.Name)
			}
		default:
			collect.Errorf("unknown bucket %q", bucket)
			return
		}
		assert.Contains(collect, names, name,
			"expected %q in %s=%v (body=%s)", name, bucket, names, string(b))
	}, timeout, 250*time.Millisecond,
		"%q never reached %s state at %s", name, bucket, healthURL)
}
