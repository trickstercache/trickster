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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	fso "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	graphiteWebAddr = "127.0.0.1:8081"
	carbonAddr      = "127.0.0.1:2003"
	graphiteBackend = "graphite1"
	// the dev env's dev.fast.* ladder is 10s:6h,60s:7d,10m:5y, so a query on
	// either side of the 6h rung boundary is answered at a different step
	fastRungBoundary = 6 * time.Hour
)

// a series the developer environment's generator keeps current
type graphiteSeries struct {
	target string
	step   time.Duration // native step of the finest rung
}

var (
	fastHost01 = graphiteSeries{"dev.fast.cpu.host01.percent", 10 * time.Second}
	fastLeaves = []string{
		"dev.fast.cpu.host01.percent", "dev.fast.cpu.host02.percent",
		"dev.fast.latency.api.p99", "dev.fast.requests.api.count",
	}
)

// isolates the graphite backend for one test: a private filesystem cache
// (carried across restarts by reusing cacheDir) and a short find cache TTL
func graphiteConfig(cacheDir string, persist bool) func(*tkconfig.Config) {
	return func(c *tkconfig.Config) {
		cache := co.New()
		cache.Name = "graphite_it"
		cache.Provider = "filesystem"
		cache.Filesystem = &fso.Options{CachePath: cacheDir}
		// a restart reads cached objects through the on-disk index; the default
		// 5s flush interval would make the restart tests wait on a timer
		cache.Index.FlushInterval = timeconv.Duration(250 * time.Millisecond)
		c.Caches["graphite_it"] = cache
		b := c.Backends[graphiteBackend]
		b.CacheName = "graphite_it"
		// the config is read here before defaulting runs, so the provider
		// block is only present when the file spells it out
		if b.Graphite == nil {
			b.Graphite = gro.New()
		}
		b.Graphite.ResolutionRegistry.Persist = persist
		b.Graphite.FindCacheTTL = timeconv.Duration(2 * time.Second)
	}
}

// boots an isolated Trickster against the dev environment's Graphite; the
// stop func lets a test restart it against the same cache directory
func startGraphite(t *testing.T, cacheDir string, persist bool) (tricksterHarness, func()) {
	t.Helper()
	h := configHarness(t, graphiteConfig(cacheDir, persist))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if h.releasePorts != nil {
		h.releasePorts()
	}
	go startTrickster(t, ctx, expectedStartError{}, "-config", h.ConfigPath)
	waitForTrickster(t, h.MetricsAddr)
	return h, cancel
}

func renderParams(target, from, until string) url.Values {
	return url.Values{"target": {target}, "from": {from}, "until": {until}, "format": {"json"}}
}

type graphiteSeriesJSON struct {
	Target     string            `json:"target"`
	Datapoints [][2]*float64     `json:"datapoints"`
	Tags       map[string]string `json:"tags"`
}

// issues a render through Trickster and returns the decoded series with
// the X-Trickster-Result fields
func renderThroughTrickster(t *testing.T, h tricksterHarness,
	params url.Values,
) ([]graphiteSeriesJSON, map[string]string) {
	t.Helper()
	resp, body := h.do(t, "/"+graphiteBackend+"/render", withParams(params))
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
	var out []graphiteSeriesJSON
	require.NoError(t, json.Unmarshal(body, &out), "body: %.240s", string(body))
	return out, parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
}

// reads the step the origin actually answered at: the spacing of the
// returned datapoints
func observedStep(t *testing.T, series []graphiteSeriesJSON) time.Duration {
	t.Helper()
	require.NotEmpty(t, series, "no series returned")
	pts := series[0].Datapoints
	require.GreaterOrEqual(t, len(pts), 2, "need two points to observe a step")
	require.NotNil(t, pts[0][1])
	require.NotNil(t, pts[1][1])
	return time.Duration(*pts[1][1]-*pts[0][1]) * time.Second
}

// repeats the request until the resolution registry has learned the ladder;
// learning is in the background, so early requests are unaccelerated by design
func waitForDelta(t *testing.T, h tricksterHarness, params url.Values) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, body := h.do(t, "/"+graphiteBackend+"/render", withParams(params))
		if !assert.Equal(collect, http.StatusOK, resp.StatusCode, "body: %.120s", string(body)) {
			return
		}
		got := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		assert.Equal(collect, "DeltaProxyCache", got["engine"],
			"still unaccelerated: %s", resp.Header.Get("X-Trickster-Result"))
	}, 90*time.Second, time.Second, "the ladder was never learned")
}

// waits for the full ladder: only a complete ladder knows maxRetention, so a
// delta answer to a far-past query proves completeness; only those persist
func waitForCompleteLadder(t *testing.T, h tricksterHarness, target string) {
	t.Helper()
	waitForDelta(t, h, renderParams(target, "-10y", "-5min"))
}

// sums the samples of one Graphite metric family for the graphite1 backend,
// optionally filtered to samples containing match
func graphiteMetric(t *testing.T, metricsAddr, family, match string) float64 {
	t.Helper()
	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var total float64
	prefix := "trickster_graphite_" + family + "{"
	for line := range strings.SplitSeq(string(b), "\n") {
		if !strings.HasPrefix(line, prefix) ||
			!strings.Contains(line, `backend_name="`+graphiteBackend+`"`) {
			continue
		}
		if match != "" && !strings.Contains(line, match) {
			continue
		}
		fields := strings.Fields(line[strings.LastIndex(line, "}")+1:])
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			total += v
		}
	}
	return total
}

// the on-disk index a restart reads objects through: the filesystem cache
// turns a key's dots into ~4 and suffixes every file with "data"
func cacheIndexPath(dir string) string { return filepath.Join(dir, "cache~4indexdata") }

// waits until the filesystem cache has written its index since the given
// moment; an object stored but not yet indexed is invisible after a restart
func waitForIndexFlush(t *testing.T, dir string, since time.Time) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		fi, err := os.Stat(cacheIndexPath(dir))
		if !assert.NoError(collect, err) {
			return
		}
		assert.False(collect, fi.ModTime().Before(since), "the cache index has not been flushed yet")
	}, 30*time.Second, 100*time.Millisecond, "the cache index was never written to %s", dir)
}

// writes points to carbon over the plaintext protocol, at step spacing ending
// at the most recent step boundary, so a recent render window sees them
func feedCarbon(t *testing.T, metric string, step time.Duration, points int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", carbonAddr, 5*time.Second)
	require.NoError(t, err, "carbon is not reachable at %s", carbonAddr)
	defer conn.Close()
	end := time.Now().Truncate(step)
	var buf strings.Builder
	for i := points; i > 0; i-- {
		ts := end.Add(-time.Duration(i) * step).Unix()
		fmt.Fprintf(&buf, "%s %d %d\n", metric, i, ts)
	}
	require.NoError(t, conn.SetWriteDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(conn, buf.String())
	require.NoError(t, err)
}

// waits for a metric written to carbon to be visible at the origin; carbon
// buffers writes and creates the whisper file on its own schedule
func waitForGraphiteMetric(t *testing.T, metric string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get("http://" + graphiteWebAddr +
			"/metrics/expand?leavesOnly=1&query=" + url.QueryEscape(metric))
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		assert.Contains(collect, string(b), metric, "carbon has not created %s yet", metric)
	}, 90*time.Second, 2*time.Second, "%s never appeared at the origin", metric)
}

func TestGraphite(t *testing.T) {
	waitForGraphiteData(t, graphiteWebAddr)

	t.Run("cold miss, learn, warm hit", func(t *testing.T) {
		h, _ := startGraphite(t, t.TempDir(), false)
		params := renderParams(fastHost01.target, "-30min", "-5min")

		// nothing is known about this metric yet, so the request is served
		// correctly through the object lane rather than guessed at
		series, first := renderThroughTrickster(t, h, params)
		require.NotEmpty(t, series)
		require.Equal(t, "ObjectProxyCache", first["engine"],
			"an unlearned metric must not be delta cached")

		// the learner converges in the background
		waitForDelta(t, h, params)

		_, hit := renderThroughTrickster(t, h, params)
		require.Equal(t, "DeltaProxyCache", hit["engine"])
		require.Equal(t, "hit", hit["status"])
	})

	t.Run("delta fetch across a partial range", func(t *testing.T) {
		h, _ := startGraphite(t, t.TempDir(), false)
		narrow := renderParams(fastHost01.target, "-30min", "-5min")
		waitForDelta(t, h, narrow)
		renderThroughTrickster(t, h, narrow)

		// a wider window over the same metric: the cache already holds the
		// middle, so only the wings are fetched
		wide := renderParams(fastHost01.target, "-90min", "-5min")
		series, got := renderThroughTrickster(t, h, wide)
		require.Equal(t, "DeltaProxyCache", got["engine"])
		require.Equal(t, "phit", got["status"], "expected a partial hit, got %v", got)
		require.NotEmpty(t, series)
		require.Equal(t, fastHost01.step, observedStep(t, series))

		_, again := renderThroughTrickster(t, h, wide)
		require.Equal(t, "hit", again["status"])
	})

	t.Run("archive boundary crossing", func(t *testing.T) {
		h, _ := startGraphite(t, t.TempDir(), false)
		// a second either side of the 6h rung boundary: whisper selects on
		// retention >= now-from, so these differ in step and are cached apart
		inside := renderParams(fastHost01.target,
			fmt.Sprintf("-%ds", int(fastRungBoundary.Seconds())-1), "-5min")
		outside := renderParams(fastHost01.target,
			fmt.Sprintf("-%ds", int(fastRungBoundary.Seconds())+1), "-5min")
		waitForDelta(t, h, inside)

		fine, got := renderThroughTrickster(t, h, inside)
		require.Equal(t, "DeltaProxyCache", got["engine"])
		require.Equal(t, 10*time.Second, observedStep(t, fine))

		coarse, got2 := renderThroughTrickster(t, h, outside)
		require.Equal(t, "DeltaProxyCache", got2["engine"])
		require.Equal(t, time.Minute, observedStep(t, coarse),
			"a query past the rung boundary must be answered at the next rung's step")
		// a different step is a different cache key, so this cannot have
		// been served from the finer window's entry
		require.NotEqual(t, "hit", got2["status"])

		require.Zero(t, graphiteMetric(t, h.MetricsAddr, "step_mispredictions_total", ""),
			"the step was mispredicted at a rung boundary")
	})

	t.Run("wildcard expansion picks up a new leaf", func(t *testing.T) {
		h, _ := startGraphite(t, t.TempDir(), false)
		// a namespace of this test's own, so the dev env's dev.* series (and
		// the dashboards over them) are untouched
		ns := fmt.Sprintf("it.expand.%d", time.Now().Unix())
		alpha, beta := ns+".alpha.value", ns+".beta.value"
		feedCarbon(t, alpha, time.Minute, 20)
		waitForGraphiteMetric(t, alpha)

		wildcard := renderParams(ns+".*.value", "-20min", "-1min")
		series, _ := renderThroughTrickster(t, h, wildcard)
		require.Len(t, series, 1, "expected only %s", alpha)

		// a new leaf changes what the wildcard means; once the expansion TTL
		// lapses the next render must see it, not the stale expansion
		feedCarbon(t, beta, time.Minute, 20)
		waitForGraphiteMetric(t, beta)
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			resp, body := h.do(t, "/"+graphiteBackend+"/render", withParams(wildcard))
			if !assert.Equal(collect, http.StatusOK, resp.StatusCode) {
				return
			}
			var out []graphiteSeriesJSON
			if !assert.NoError(collect, json.Unmarshal(body, &out)) {
				return
			}
			assert.Len(collect, out, 2, "the new leaf never entered the expansion")
		}, 60*time.Second, 2*time.Second, "the wildcard expansion never refreshed")
	})

	t.Run("registry persistence across a restart", func(t *testing.T) {
		params := renderParams("dev.medium.orders.us-east.count", "-30min", "-5min")

		t.Run("persisted", func(t *testing.T) {
			dir := t.TempDir()
			h, stop := startGraphite(t, dir, true)
			waitForDelta(t, h, params)
			learned := time.Now()
			waitForCompleteLadder(t, h, "dev.medium.orders.us-east.count")
			waitForIndexFlush(t, dir, learned)
			stop()

			// the same cache, a new process: the ladder was written through,
			// so the very first render is accelerated with no relearning
			h2, _ := startGraphite(t, dir, true)
			_, got := renderThroughTrickster(t, h2, params)
			require.Equal(t, "DeltaProxyCache", got["engine"],
				"a persisted registry must survive a restart")
		})

		t.Run("not persisted", func(t *testing.T) {
			dir := t.TempDir()
			h, stop := startGraphite(t, dir, false)
			waitForDelta(t, h, params)
			stop()

			// nothing was written through, so the new process starts blind
			// and falls back to the object lane until it relearns
			h2, _ := startGraphite(t, dir, false)
			_, got := renderThroughTrickster(t, h2, params)
			require.Equal(t, "ObjectProxyCache", got["engine"],
				"without persistence the registry must start empty")
			waitForDelta(t, h2, params)
		})
	})

	t.Run("concurrent cold start learns each ladder once", func(t *testing.T) {
		h, _ := startGraphite(t, t.TempDir(), false)
		before := graphiteMetric(t, h.MetricsAddr, "probes_total", "")

		// the dashboard stampede: every panel refreshes at once against a cold
		// registry; the one shared ladder must be discovered exactly once
		var wg sync.WaitGroup
		for range 4 {
			for _, leaf := range fastLeaves {
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp, _ := h.do(t, "/"+graphiteBackend+"/render",
						withParams(renderParams(leaf, "-30min", "-5min")))
					assert.Equal(t, http.StatusOK, resp.StatusCode)
				}()
			}
		}
		wg.Wait()
		for _, leaf := range fastLeaves {
			waitForDelta(t, h, renderParams(leaf, "-30min", "-5min"))
		}

		// one discovery (~a dozen probes) plus four confirmations (2n+3 each)
		// fits under the ceiling; per-leaf rediscovery (~150 probes) does not
		spent := graphiteMetric(t, h.MetricsAddr, "probes_total", "") - before
		const ceiling = 60
		require.LessOrEqual(t, spent, float64(ceiling),
			"%.0f probes for %d leaves on one ladder: the ladder was rediscovered per leaf",
			spent, len(fastLeaves))
		t.Logf("%d leaves on one ladder cost %.0f probes; ladders known: %.0f",
			len(fastLeaves), spent, graphiteMetric(t, h.MetricsAddr, "ladders", ""))
	})
}
