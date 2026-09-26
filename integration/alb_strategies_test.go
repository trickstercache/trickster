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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

// strategyALB runs Trickster with one ALB of the given mechanism over a pool of stubs.
// extra is appended, already indented, under the alb block; slow names pool members that
// answer with simulated latency.
type strategyALB struct {
	mech  string
	extra string
	stubs []*flappingStub
	slow  map[int]string
	front int
}

func startStrategyALB(t *testing.T, mech, extra string, poolSize int, slow map[int]string) *strategyALB {
	t.Helper()
	ports, release := portutil.Reserve(t, 3)
	a := &strategyALB{mech: mech, extra: extra, slow: slow, front: ports[0]}
	a.stubs = make([]*flappingStub, poolSize)
	for i := range a.stubs {
		a.stubs[i] = newFlappingStub(t, fmt.Sprintf("p%d", i), true)
	}
	var sb strings.Builder
	sb.WriteString(promstub.Preamble(ports[0], ports[1], ports[2]))
	sb.WriteString("backends:\n")
	for i, s := range a.stubs {
		sb.WriteString(promstub.BackendStanza(fmt.Sprintf("prom%d", i), s.URL()))
		if d, ok := slow[i]; ok {
			fmt.Fprintf(&sb, "    latency_min: %s\n    latency_max: %s\n", d, d)
		}
	}
	sb.WriteString("  alb-strategy:\n    provider: alb\n    alb:\n")
	fmt.Fprintf(&sb, "      mechanism: %s\n", mech)
	sb.WriteString("      healthy_floor: 1\n")
	sb.WriteString(extra)
	sb.WriteString("      pool:\n")
	for i := range a.stubs {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(sb.String()), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	runTrickster(t, ctx, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", ports[1]))
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/trickster/health", ports[1])
	for i := range a.stubs {
		requireHealthState(t, healthURL, fmt.Sprintf("prom%d", i), "available", 10*time.Second)
	}
	return a
}

// status sends one instant query, distinct per n so no cache answers it, and returns the
// response code
func (a *strategyALB) status(t *testing.T, n int, header http.Header) int {
	t.Helper()
	q := url.Values{"query": {fmt.Sprintf(`up{n="%d"}`, n)}}
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-strategy/api/v1/query?%s", a.front, q.Encode())
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// query is status for a pool whose every member is up: anything but a 200 fails the test
func (a *strategyALB) query(t *testing.T, n int, header http.Header) {
	t.Helper()
	require.Equal(t, http.StatusOK, a.status(t, n, header), "%s request %d", a.mech, n)
}

func (a *strategyALB) hits() []int64 {
	out := make([]int64, len(a.stubs))
	for i, s := range a.stubs {
		out[i] = s.hits.Load()
	}
	return out
}

// every strategy serves every request from a healthy pool, reaches all of its members, and
// stops routing to a member once its health check fails
func TestALBStrategiesServeAndFollowHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster per mechanism; skipping in -short mode")
	}
	for _, mech := range []string{"p2c", "lc", "lt", "hrw", "rr"} {
		t.Run(mech, func(t *testing.T) {
			// every test client is 127.0.0.1, so the mechanism that routes by key is given one
			// that varies
			extra := ""
			if mech == "hrw" {
				extra = "      hrw:\n        key: header:X-Client\n"
			}
			a := startStrategyALB(t, mech, extra, 3, nil)
			for i := range 60 {
				a.query(t, i, http.Header{"X-Client": {fmt.Sprintf("client-%d", i)}})
			}
			if mech != "lt" {
				// lt rightly favors whichever member answered fastest; the rest spread
				for i, n := range a.hits() {
					require.NotZero(t, n, "%s never routed to pool member %d of 3: %v", mech, i, a.hits())
				}
			}

			// until its health check notices, the downed member still answers its share, with
			// errors; after that it must get nothing and every request must succeed
			a.stubs[0].setUp(false)
			n := 1000
			require.Eventually(t, func() bool {
				start := a.stubs[0].hits.Load()
				clean := true
				for range 12 {
					n++
					clean = a.status(t, n, http.Header{"X-Client": {fmt.Sprintf("client-%d", n)}}) == http.StatusOK && clean
				}
				return clean && a.stubs[0].hits.Load() == start
			}, 15*time.Second, 200*time.Millisecond,
				"%s kept routing to a member whose health check fails", mech)
		})
	}
}

// with hrw keyed on a header, a tenant's requests all reach one member, and tenants spread
func TestALBHRWStickiness(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	a := startStrategyALB(t, "hrw", "      hrw:\n        key: header:X-Tenant\n", 4, nil)
	owners := make(map[int]bool)
	n := 0
	for tenant := range 12 {
		before := a.hits()
		for range 5 {
			a.query(t, n, http.Header{"X-Tenant": {fmt.Sprintf("tenant-%d", tenant)}})
			n++
		}
		var served []int
		for i, h := range a.hits() {
			if h != before[i] {
				served = append(served, i)
				require.EqualValues(t, 5, h-before[i], "tenant-%d was split across members", tenant)
			}
		}
		require.Len(t, served, 1, "tenant-%d reached members %v", tenant, served)
		owners[served[0]] = true
	}
	require.GreaterOrEqual(t, len(owners), 3, "12 tenants landed on only %d of 4 members", len(owners))
}

// lt learns which member answers faster and sends it the bulk of sequential traffic
func TestALBLeastTimePrefersTheFasterMember(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	a := startStrategyALB(t, "lt", "", 2, map[int]string{0: "150ms"})
	for i := range 40 {
		a.query(t, i, nil)
	}
	hits := a.hits()
	require.Greater(t, hits[1], 3*hits[0],
		"the member without simulated latency took %d of 40, the slow one %d", hits[1], hits[0])
	require.NotZero(t, hits[0], "the slow member was never tried")
}
