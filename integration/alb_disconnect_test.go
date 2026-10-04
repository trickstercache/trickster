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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

type disconnectStub struct {
	srv      *httptest.Server
	gate     <-chan struct{}
	started  chan struct{}
	finished chan struct{}
	active   atomic.Int32
}

func newDisconnectStub(t *testing.T, label string) *disconnectStub {
	t.Helper()
	s := &disconnectStub{started: make(chan struct{}, 1), finished: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.Handle(promstub.BuildInfoPath, promstub.BuildInfoHandler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.active.Add(1)
		defer s.active.Add(-1)
		select {
		case s.started <- struct{}{}:
		default:
		}
		defer func() {
			select {
			case s.finished <- struct{}{}:
			default:
			}
		}()
		if s.gate != nil {
			select {
			case <-s.gate:
			case <-r.Context().Done():
				return
			}
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

func (s *disconnectStub) URL() string { return s.srv.URL }

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
// the ALB mechanism.
func runDisconnectMidFanout(t *testing.T, mech string) {
	t.Helper()

	ports, releasePorts := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]

	const stubs = 3
	stubsArr := make([]*disconnectStub, stubs)
	for i := range stubsArr {
		stubsArr[i] = newDisconnectStub(t, fmt.Sprintf("p%d", i))
	}

	slowGate := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(slowGate) })
	defer unblock()
	stubsArr[1].gate, stubsArr[2].gate = slowGate, slowGate

	var sb strings.Builder
	fmt.Fprintf(&sb, "listeners:\n  default:\n    port: %d\n", frontPort)
	fmt.Fprintf(&sb, "  metrics:\n    port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "  mgmt:\n    port: %d\n", mgmtPort)
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
	releasePorts()
	runTrickster(t, ctx, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	// Let startup workers settle before sampling the request's goroutine growth.
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
	t.Cleanup(cancelReq)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	require.NoError(t, err)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	// Fire the request in a goroutine so we can cancel from the main path.
	type result struct {
		resp *http.Response
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		resCh <- result{resp: resp, err: err}
	}()

	for _, stub := range stubsArr {
		select {
		case <-stub.started:
		case <-time.After(10 * time.Second):
			t.Fatal("fanout did not reach every origin")
		}
	}
	select {
	case <-stubsArr[0].finished:
	case <-time.After(10 * time.Second):
		t.Fatal("first origin did not finish responding")
	}
	cancelReq()

	select {
	case r := <-resCh:
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			t.Logf("%s: client returned non-cancel error after disconnect: %v", mech, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: client.Do did not return within 3s of cancel; trickster likely hung", mech)
	}

	unblock()
	require.Eventually(t, func() bool {
		for _, stub := range stubsArr {
			if stub.active.Load() != 0 {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Millisecond, "origin handlers did not exit")
	const (
		allowedDelta = 10
		pollDeadline = 5 * time.Second
		pollInterval = 100 * time.Millisecond
	)
	// the stubs have answered by now; the idle keep-alive connections left behind, and their goroutines
	// on both ends, are test scaffolding rather than a leak
	for _, s := range stubsArr {
		s.srv.CloseClientConnections()
	}
	client.CloseIdleConnections()

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

}

func TestALB_TSM_ClientDisconnectMidFanout(t *testing.T) {
	runDisconnectMidFanout(t, "tsm")
}

func TestALB_NLM_ClientDisconnectMidFanout(t *testing.T) {
	runDisconnectMidFanout(t, "nlm")
}
