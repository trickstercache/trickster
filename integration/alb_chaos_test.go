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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/integration/internal/chaos"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"
)

func TestALBChaosBehaviors(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos matrix is slow; skipping in -short mode")
	}

	type cell struct {
		mech     string
		behavior string
		fn       http.HandlerFunc
	}
	cells := []cell{
		{"fr", "panic", chaos.BehaviorPanic()},
		{"fr", "truncate_stale_cl", chaos.BehaviorTruncateStaleCL(4096, 16)},
		{"fr", "slow_probe", chaos.BehaviorSlowProbe(2 * time.Second)},
		{"nlm", "panic", chaos.BehaviorPanic()},
		{"nlm", "truncate_stale_cl", chaos.BehaviorTruncateStaleCL(4096, 16)},
		{"nlm", "5xx_with_lm", chaos.Behavior5xxWithLM(http.StatusInternalServerError,
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))},
	}

	for _, c := range cells {
		ports, release := portutil.Reserve(t, 3)
		front, metrics, mgmt := ports[0], ports[1], ports[2]
		t.Run(fmt.Sprintf("%s_%s", c.mech, c.behavior), func(t *testing.T) {
			runChaosCell(t, c.mech, c.behavior, c.fn, front, metrics, mgmt, release)
		})
	}
}

func runChaosCell(t *testing.T, mech, behavior string, chaosData http.HandlerFunc,
	frontPort, metricsPort, mgmtPort int, releasePorts func(),
) {
	t.Helper()

	healthy := newPathAwareStub(t, chaos.BehaviorOK(promVectorBody("healthy")))
	misbehaver := newPathAwareStub(t, chaosData)

	cfgPath := writeChaosConfig(t, mech, frontPort, metricsPort, mgmtPort,
		healthy.URL, misbehaver.URL)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if releasePorts != nil {
		releasePorts()
	}
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	time.Sleep(800 * time.Millisecond)

	cli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        4,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	const reqs = 5
	const healthyBodyLen = 100 // promVectorBody is ~140 bytes; below this implies a truncated chaos win
	var ok, nonOK, short int
	for i := range reqs {
		q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano()+int64(i))
		u := fmt.Sprintf("http://127.0.0.1:%d/alb-%s/api/v1/query?query=%s",
			frontPort, mech, url.QueryEscape(q))
		resp, err := cli.Get(u)
		if err != nil {
			t.Logf("%s/%s: request %d transport err=%v", mech, behavior, i, err)
			nonOK++
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			ok++
			if len(body) < healthyBodyLen {
				short++
				t.Logf("%s/%s: short 200 body (%d bytes): %q",
					mech, behavior, len(body), string(body))
			}
		} else {
			nonOK++
		}
	}
	t.Logf("%s/%s: ok=%d nonOK=%d short=%d", mech, behavior, ok, nonOK, short)

	assert.Equalf(t, reqs, ok+nonOK,
		"%s/%s: %d/%d requests dropped (transport-level failure)",
		mech, behavior, reqs-(ok+nonOK), reqs)
	if behavior == "truncate_stale_cl" {
		assert.Zerof(t, short,
			"%s/%s: served %d short bodies; chaos stub should be disqualified by short-read check",
			mech, behavior, short)
	}
}

// pathAwareStub serves a fixed healthy 200 on /api/v1/status/buildinfo and
// routes all other paths to the supplied data handler, so chaos handlers
// can misbehave on data queries while still passing healthcheck.
type pathAwareStub struct {
	srv *httptest.Server
	URL string
}

func newPathAwareStub(t *testing.T, data http.HandlerFunc) *pathAwareStub {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.0"}}`))
	})
	mux.Handle("/", data)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &pathAwareStub{srv: srv, URL: srv.URL}
}

func promVectorBody(job string) string {
	return fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"__name__":"up","job":%q},"value":[1700000000,"1"]}]}}`,
		job)
}

func writeChaosConfig(t *testing.T, mech string, frontPort, metricsPort, mgmtPort int,
	healthyURL, chaosURL string,
) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(promstub.Preamble(frontPort, metricsPort, mgmtPort))
	sb.WriteString("backends:\n")
	for i, u := range []string{healthyURL, chaosURL} {
		sb.WriteString(promstub.BackendStanza(fmt.Sprintf("prom%d", i), u))
	}
	fmt.Fprintf(&sb, "  alb-%s:\n", mech)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	fmt.Fprintf(&sb, "      mechanism: %s\n", mech)
	sb.WriteString("      pool:\n")
	for i := range 2 {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sb.String()), 0o600))
	return cfgPath
}
