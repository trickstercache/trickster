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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
)

// TestALBPoolMemberWithoutHealthcheckNotRouted verifies that a non-virtual ALB
// pool member configured without a `healthcheck:` block does not receive
// fanout traffic when its upstream is unhealthy. The provider's default
// healthcheck must be auto-applied so the pool's dispatch-time filter can
// observe Failing status and exclude the member.
func TestALBPoolMemberWithoutHealthcheckNotRouted(t *testing.T) {
	healthyResp := albTestdata(t, "alb_unavail/healthy.json")

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
		case "/api/v1/query":
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		default:
			fmt.Fprint(w, healthyResp)
		}
	}))
	t.Cleanup(healthy.Close)

	var brokenDataHits atomic.Int64
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query_range" {
			brokenDataHits.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)

	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]

	yaml := fmt.Sprintf(albTestdata(t, "alb_missing_hc/missing_hc.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, healthy.URL, broken.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	// Give the auto-applied probe time to fire, trip the broken backend to
	// Failing, and let the ALB pool refresh its healthy snapshot.
	time.Sleep(2 * time.Second)
	dataBaseline := brokenDataHits.Load()

	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)
	const reqs = 50
	now := time.Now().Unix()
	for i := range reqs {
		// query_range is mergeable in TSM and therefore triggers fanout to
		// every healthy pool member. Use unique query strings so the cache
		// doesn't short-circuit subsequent requests.
		q := url.Values{
			"query": {fmt.Sprintf("up + 0*%d", i)},
			"start": {fmt.Sprintf("%d", now-300)},
			"end":   {fmt.Sprintf("%d", now)},
			"step":  {"15"},
		}
		u := fmt.Sprintf("http://%s/alb-tsm-test/api/v1/query_range?%s",
			frontAddr, q.Encode())
		resp, err := http.Get(u)
		require.NoError(t, err)
		resp.Body.Close()
	}

	// Allow a small drain window in case any in-flight fanout slot was still
	// dispatching when the assertion fired.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		dataDelta := brokenDataHits.Load() - dataBaseline
		assert.Equalf(collect, int64(0), dataDelta,
			"broken backend received %d data requests via the ALB after probe transitioned it to Failing",
			dataDelta)
	}, 3*time.Second, 200*time.Millisecond)
}
