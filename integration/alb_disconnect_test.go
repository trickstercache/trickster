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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/integration/promstub"
)

// Mid-fanout client disconnects are hardened in fanout.All (PR #1001). The FR
// mechanism has an in-process unit equivalent (TestHandleFirstResponseContextCancel)
// but TSM and NLM didn't have integration coverage that exercises a real
// http.Client cancelling against a real Trickster instance.
//
// These tests boot 3 slow stub Prometheus upstreams, issue a request through
// the TSM (or NLM) ALB, cancel the client context after the first upstream has
// responded but before the others have, and verify:
//  1. ServeHTTP returns within a bounded window (no hang) after the client
//     disconnects.
//  2. The goroutine count returns close to baseline once cleanup completes
//     (no leaked per-shard goroutines).
//  3. No panic appears in the test output.

type disconnectStub struct {
	srv    *httptest.Server
	delay  atomic.Int64 // nanoseconds; per-request sleep before responding
	served atomic.Int64
}

func newDisconnectStub(t *testing.T, label string) *disconnectStub {
	t.Helper()
	s := &disconnectStub{}
	mux := http.NewServeMux()
	mux.Handle(promstub.BuildInfoPath, promstub.BuildInfoHandler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		d := time.Duration(s.delay.Load())
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		s.served.Add(1)
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
			_, _ = fmt.Fprint(w, mkDisconnectMatrix(label, start, end, step))
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

func (s *disconnectStub) setDelay(d time.Duration) { s.delay.Store(int64(d)) }
func (s *disconnectStub) URL() string              { return s.srv.URL }

func mkDisconnectMatrix(label string, start, end, step int64) string {
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

// runDisconnectMidFanout shares the body of the TSM/NLM tests. mech selects
// the ALB mechanism, frontPort/metricsPort/mgmtPort select disjoint ports per
// test so the suite stays parallel-safe.
func runDisconnectMidFanout(t *testing.T, mech string, frontPort, metricsPort, mgmtPort int) {
	t.Helper()

	const stubs = 3
	stubsArr := make([]*disconnectStub, stubs)
	for i := range stubsArr {
		stubsArr[i] = newDisconnectStub(t, fmt.Sprintf("p%d", i))
	}

	// Tiered delays so the client can cancel after ~one upstream has
	// responded but before the others have.
	stubsArr[0].setDelay(50 * time.Millisecond)
	stubsArr[1].setDelay(2 * time.Second)
	stubsArr[2].setDelay(2 * time.Second)

	var sb strings.Builder
	fmt.Fprintf(&sb, "frontend:\n  listen_port: %d\n", frontPort)
	fmt.Fprintf(&sb, "metrics:\n  listen_port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "mgmt:\n  listen_port: %d\n", mgmtPort)
	sb.WriteString("logging:\n  log_level: error\n")
	sb.WriteString("caches:\n  mem1:\n    provider: memory\n")
	sb.WriteString("backends:\n")
	for i, s := range stubsArr {
		fmt.Fprintf(&sb, "  prom%d:\n", i)
		sb.WriteString("    provider: prometheus\n")
		fmt.Fprintf(&sb, "    origin_url: %s\n", s.URL())
		sb.WriteString("    cache_name: mem1\n")
	}
	fmt.Fprintf(&sb, "  alb-%s:\n", mech)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	fmt.Fprintf(&sb, "      mechanism: %s\n", mech)
	sb.WriteString("      pool:\n")
	for i := range stubsArr {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sb.String()), 0o644))

	ctx, cancelTrickster := context.WithCancel(context.Background())
	t.Cleanup(cancelTrickster)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	// Settle goroutine count after Trickster startup (HTTP servers, listeners,
	// pool refreshers all spawn long-lived workers we don't want to attribute
	// to the request).
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	now := time.Now()
	params := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
		"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
		"step":  {"15"},
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-%s/api/v1/query_range?%s",
		frontPort, mech, params.Encode())

	reqCtx, cancelReq := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	require.NoError(t, err)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	// Fire the request in a goroutine so we can cancel from the main path.
	type result struct {
		resp *http.Response
		err  error
		when time.Time
	}
	resCh := make(chan result, 1)
	start := time.Now()
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		resCh <- result{resp: resp, err: err, when: time.Now()}
	}()

	// Wait until the first (fast) stub has responded, then cancel before the
	// slow stubs return. 150ms gives the 50ms stub plenty of headroom while
	// staying well below the 2s slow stubs.
	time.Sleep(150 * time.Millisecond)
	cancelReq()

	select {
	case r := <-resCh:
		elapsed := r.when.Sub(start)
		// We expect either context.Canceled bubbling up or a partial body.
		// What we MUST NOT see: the request hanging until the slow stubs
		// finish (~2s). Allow up to 1s as headroom for cleanup.
		require.Less(t, elapsed, time.Second,
			"%s: ServeHTTP did not return promptly after client disconnect (elapsed=%s, err=%v)",
			mech, elapsed, r.err)
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			t.Logf("%s: client returned non-cancel error after disconnect: %v", mech, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: client.Do did not return within 3s of cancel; trickster likely hung", mech)
	}

	// Slow stub upstreams (2s) need to finish naturally before sampling.
	// Until they do, their server-side handler goroutines are still parked
	// in time.After and counted as "leaks", swamping any signal from
	// trickster's own fanout cleanup. 2.5s is the floor; we poll past that
	// to absorb late-arriving cleanup (keepalive timers, capture flushers,
	// httptest accept-loop churn) without a hard sleep gamble.
	const (
		// 10 covers test-scaffolding noise (httptest accept loops, keepalive
		// timers, capture flushers) above the ~3-goroutine per-shard leak
		// threshold. Tightening further flakes on CI without flagging real
		// regressions; rely on -race + the post < baseline+10 bound here.
		allowedDelta = 10
		pollDeadline = 5 * time.Second
		pollInterval = 100 * time.Millisecond
		settleFloor  = 2500 * time.Millisecond
	)
	time.Sleep(settleFloor)

	var (
		post  int
		delta int
	)
	deadline := time.Now().Add(pollDeadline)
	for {
		runtime.GC()
		post = runtime.NumGoroutine()
		delta = post - baseline
		if delta <= allowedDelta {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}

	if delta > allowedDelta {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Logf("%s: goroutine dump on suspected leak:\n%s", mech, buf[:n])
	}
	require.LessOrEqualf(t, delta, allowedDelta,
		"%s: goroutine count grew by %d (baseline=%d, post=%d); suspected per-shard leak after client disconnect",
		mech, delta, baseline, post)

	t.Logf("%s: baseline=%d post=%d delta=%d", mech, baseline, post, delta)

	// TODO: scrape /metrics for trickster_alb_fanout_failures_total and assert
	// the canceled slots did not bump the counter (a clean disconnect should
	// not be classified as a fanout failure). Skipped here because the metric
	// has mechanism/variant/reason labels and the cancel path may legitimately
	// flag certain reasons; needs a follow-up to nail down which.
}

func TestALB_TSM_ClientDisconnectMidFanout(t *testing.T) {
	runDisconnectMidFanout(t, "tsm", 19200, 19201, 19202)
}

func TestALB_NLM_ClientDisconnectMidFanout(t *testing.T) {
	runDisconnectMidFanout(t, "nlm", 19210, 19211, 19212)
}
