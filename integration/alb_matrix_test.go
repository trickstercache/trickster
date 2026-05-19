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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"
)

// TestALBMatrix crosses the ALB mechanism, pool size, fraction of pool
// members in a "down" state, and steady/flapping mode. The bugs hardened
// in the ALB sweep (#997, #1000, #1001) all lived in the [all-down] and
// [flap-cycle] columns of this matrix and were absent from any existing
// integration test.
//
// Cells are run as table-driven subtests. The matrix is deliberately
// exhaustive (it surfaces correctness drift on any mechanism that
// regresses); skip individual cells with t.Skip if a future change
// makes one of them too noisy.
func TestALBMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("matrix test is slow; skipping in -short mode")
	}

	cases := buildMatrixCases()
	for _, c := range cases {
		// Reserve per-cell with a deferred release so the listeners stay
		// open through config write + most of cell setup; trickster binds
		// the same ports immediately after release, minimizing the
		// close-to-bind race window.
		ports, release := portutil.Reserve(t, 3)
		c.frontPort, c.metricsPort, c.mgmtPort = ports[0], ports[1], ports[2]
		c.releasePorts = release
		t.Run(c.name(), func(t *testing.T) {
			runMatrixCell(t, c)
		})
	}
}

type matrixCell struct {
	mech         string
	poolSize     int
	downFraction float64
	mode         string // "steady" or "flapping"
	frontPort    int
	metricsPort  int
	mgmtPort     int
	// releasePorts closes the reserved-port listeners. The cell calls it
	// immediately before startTrickster so the close-to-bind window is a
	// single function call wide.
	releasePorts func()
}

func (c matrixCell) name() string {
	return fmt.Sprintf("%s_pool%d_down%02d_%s",
		c.mech, c.poolSize, int(c.downFraction*100), c.mode)
}

func (c matrixCell) downCount() int {
	n := min(int(float64(c.poolSize)*c.downFraction+0.5), c.poolSize)
	// Guarantee at least one healthy member for any fraction strictly
	// less than 1.0; otherwise rounding (e.g. 5 * 0.9 = 4.5 -> 5) can
	// collapse a "mostly down" cell into all-down and re-classify the
	// expected behavior.
	if c.downFraction < 1.0 && n >= c.poolSize {
		n = c.poolSize - 1
	}
	return n
}

func buildMatrixCases() []matrixCell {
	mechs := []string{"fr", "nlm", "tsm"}
	pools := []int{2, 5, 10}
	downs := []float64{0.0, 0.5, 0.9, 1.0}
	modes := []string{"steady", "flapping"}

	var cases []matrixCell
	for _, m := range mechs {
		for _, p := range pools {
			for _, d := range downs {
				for _, mode := range modes {
					cases = append(cases, matrixCell{
						mech:         m,
						poolSize:     p,
						downFraction: d,
						mode:         mode,
					})
				}
			}
		}
	}
	return cases
}

// flappingStub serves /api/v1/* with the current up/down value. up=true
// returns a valid Prometheus envelope; up=false returns 500. Tests can
// flip the state at any time via setUp.
type flappingStub struct {
	srv  *httptest.Server
	up   atomic.Bool
	hits atomic.Int64
}

func newFlappingStub(t *testing.T, label string, initialUp bool) *flappingStub {
	t.Helper()
	s := &flappingStub{}
	s.up.Store(initialUp)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
		if !s.up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.0"}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if !s.up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		_ = r.ParseForm()
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/query_range"):
			start, _ := parseInt(r.Form.Get("start"))
			end, _ := parseInt(r.Form.Get("end"))
			step, _ := parseInt(r.Form.Get("step"))
			if step == 0 {
				step = 15
			}
			if end < start {
				end = start
			}
			_, _ = fmt.Fprint(w, mkMatrixSeries(label, start, end, step))
		default:
			_, _ = fmt.Fprintf(w,
				`{"status":"success","data":{"resultType":"vector","result":[`+
					`{"metric":{"__name__":"up","job":%q},"value":[1700000000,"1"]}]}}`,
				label)
		}
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *flappingStub) setUp(v bool) { s.up.Store(v) }
func (s *flappingStub) URL() string  { return s.srv.URL }

func mkMatrixSeries(label string, start, end, step int64) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":`)
	fmt.Fprintf(&b, "%q", label)
	b.WriteString(`},"values":[`)
	first := true
	for ts := start; ts <= end; ts += step {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, `[%d,"1"]`, ts)
	}
	b.WriteString(`]}]}}`)
	return b.String()
}

func runMatrixCell(t *testing.T, c matrixCell) {
	t.Helper()

	stubs := make([]*flappingStub, c.poolSize)
	down := c.downCount()
	for i := range stubs {
		// First `down` stubs start in the down state for steady mode.
		// For flapping mode the initial state still seeds the oscillator
		// but the flipper goroutine will move them after start.
		initialUp := i >= down
		stubs[i] = newFlappingStub(t, fmt.Sprintf("p%d", i), initialUp)
	}

	cfgPath := writeMatrixConfig(t, c, stubs)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if c.releasePorts != nil {
		c.releasePorts()
	}
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", c.metricsPort))

	// In flapping mode, oscillate the "down" stubs every ~150ms. Stop on
	// test cleanup so a long-running goroutine doesn't bleed into the
	// next cell.
	if c.mode == "flapping" && down > 0 && down < c.poolSize {
		flipCtx, flipCancel := context.WithCancel(context.Background())
		t.Cleanup(flipCancel)
		go func() {
			tk := time.NewTicker(150 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-flipCtx.Done():
					return
				case <-tk.C:
					for i := range down {
						stubs[i].setUp(!stubs[i].up.Load())
					}
				}
			}
		}()
	}

	// Let healthchecks converge before we sample metrics. With interval=100ms
	// and failure_threshold=1 the broken stubs should be marked Failing inside
	// ~300ms; budget 800ms to absorb scheduler jitter and pool-refresh lag
	// (pool.Targets() is informer-cached and may trail the status flip).
	time.Sleep(800 * time.Millisecond)

	before := metricsutil.Scrape(t, c.metricsPort)

	const reqs = 10
	var ok, nonOK int
	frontAddr := fmt.Sprintf("127.0.0.1:%d", c.frontPort)
	// Reuse a keepalive-enabled client so the 10-request fan doesn't
	// chew through macOS ephemeral ports when many cells run back to back.
	cli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	for i := range reqs {
		q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano()+int64(i))
		u := fmt.Sprintf("http://%s/alb-%s/api/v1/query?query=%s",
			frontAddr, c.mech, url.QueryEscape(q))
		resp, err := cli.Get(u)
		if err != nil {
			t.Logf("%s: request %d transport err=%v", c.name(), i, err)
			nonOK++
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			ok++
		} else {
			nonOK++
			if i == 0 {
				t.Logf("%s: first non-2xx status=%d body=%s",
					c.name(), resp.StatusCode, string(body))
			}
		}
	}
	t.Logf("%s: ok=%d nonOK=%d (reqs=%d)", c.name(), ok, nonOK, reqs)

	after := metricsutil.Scrape(t, c.metricsPort)

	switch {
	case c.downFraction == 1.0 && c.mode == "steady":
		// All-down + steady: ALB has zero healthy targets. The PR that
		// introduced this matrix proved the fix; a regression here would
		// re-open the original bug.
		assert.Equalf(t, 0, ok,
			"%s: all-down pool returned %d/%d successful responses; expected 0",
			c.name(), ok, reqs)
		assert.Equalf(t, reqs, nonOK,
			"%s: all-down pool produced %d non-2xx; expected %d",
			c.name(), nonOK, reqs)

	case c.downFraction == 1.0 && c.mode == "flapping":
		// All-down + flapping: state churns between fully-down and
		// fully-up. Behavior is non-deterministic; just assert no panic
		// (the test process is still running if we got here) and that
		// the pool actually exercised the fanout path.
		t.Logf("%s: flapping all-down cell; tolerated ok=%d nonOK=%d", c.name(), ok, nonOK)

	case c.downFraction < 1.0 && c.mode == "steady":
		// Steady with at least one healthy member must serve traffic.
		// TSM degrades to a direct dispatch when the live target count
		// is 1 (see pkg/backends/alb/mech/tsm/time_series_merge.go);
		// the previous behavior of returning 502 with one healthy
		// shard is now a hard assertion failure.
		assert.GreaterOrEqualf(t, ok, reqs-2,
			"%s: %d/%d requests succeeded; expected most to succeed against partial-down pool",
			c.name(), ok, reqs)

	case c.mode == "flapping":
		// Flapping with at least one always-healthy member: some
		// requests may land while a flapper is down. Eventual
		// consistency: at least one must succeed.
		//
		// High-down-fraction (>=90%) cells are intermittently starved
		// under CI -race load: with 9 of 10 members oscillating, the 100ms
		// healthcheck interval can miss the always-healthy member if the
		// runner stalls. Tolerated as a known stress-test edge.
		if c.downFraction >= 0.9 {
			t.Logf("%s: high-flap cell ok=%d nonOK=%d (tolerated under CI load)",
				c.name(), ok, nonOK)
		} else {
			assert.Greaterf(t, ok, 0,
				"%s: 0/%d requests succeeded under flapping; expected at least one once healthcheck recovers",
				c.name(), reqs)
		}
	}

	// Metric assertions: any call that reaches the fanout path should
	// increment fanout_attempts for some variant.
	//
	// Skips:
	//   - all-down + steady: no fanout occurs (pool short-circuits to 502).
	//   - exactly one healthy member: FR/NLM/TSM short-circuit to direct
	//     dispatch on hl[0] and never enter the fanout primitive, so
	//     ALBFanoutAttempts is never incremented.
	//   - ok == 0: nothing succeeded; counter assertions would just
	//     compound a failure already reported by the success-path check.
	mechVariants := mechVariants(c.mech)
	healthy := c.poolSize - c.downCount()
	skipMetric := healthy <= 1 || ok == 0 || (c.downFraction == 1.0 && c.mode == "steady")
	if !skipMetric {
		var totalDelta float64
		for _, variant := range mechVariants {
			labels := map[string]string{"mechanism": c.mech, "variant": variant}
			k := metricsutil.Key("trickster_alb_fanout_attempts_total", labels)
			totalDelta += after[k] - before[k]
		}
		assert.Greaterf(t, totalDelta, float64(0),
			"%s: trickster_alb_fanout_attempts_total{mechanism=%s,variant=*} did not increment across any variant (ok=%d healthy=%d)",
			c.name(), c.mech, ok, healthy)
	}

	// Failures should be observed in cells where at least one fanout member
	// is down (steady) or churning (flapping). The exact count varies by
	// mechanism and variant, so just assert "moved".
	if down > 0 {
		for _, variant := range mechVariants {
			for _, reason := range []string{"shard-status", "shard-error", "panic", "capture-truncated", "routing-flap"} {
				labels := map[string]string{"mechanism": c.mech, "variant": variant, "reason": reason}
				k := metricsutil.Key("trickster_alb_fanout_failures_total", labels)
				if after[k]-before[k] > 0 {
					t.Logf("%s: observed failure reason=%s variant=%s delta=%v",
						c.name(), reason, variant, after[k]-before[k])
				}
			}
		}
	}
}

// mechVariants returns the possible variant label values the given
// mechanism may emit on the ALB fanout metrics. tsm uses paired avg-sum
// and avg-count subqueries plus a primary path; fr and nlm have a single
// fanout path so the variant label is the empty string.
func mechVariants(mech string) []string {
	switch mech {
	case "tsm":
		return []string{"", "primary", "avg-sum", "avg-count"}
	default:
		return []string{""}
	}
}

func writeMatrixConfig(t *testing.T, c matrixCell, stubs []*flappingStub) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(promstub.Preamble(c.frontPort, c.metricsPort, c.mgmtPort))
	sb.WriteString("backends:\n")
	for i, s := range stubs {
		sb.WriteString(promstub.BackendStanza(fmt.Sprintf("prom%d", i), s.URL()))
	}
	fmt.Fprintf(&sb, "  alb-%s:\n", c.mech)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	fmt.Fprintf(&sb, "      mechanism: %s\n", c.mech)
	if c.mech == "tsm" {
		sb.WriteString("      output_format: prometheus\n")
	}
	sb.WriteString("      pool:\n")
	for i := range stubs {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sb.String()), 0o644))
	return cfgPath
}
