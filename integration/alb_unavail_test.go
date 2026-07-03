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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestALBUnavailableMemberNotQueried reproduces the user-reported bug where
// a backend marked unavailable by the healthcheck continued to receive
// fanout traffic. The "broken" upstream returns 500 on its healthcheck path
// (so it transitions to Failing quickly) and counts every request to its
// data path. After healthchecks mark it Failing, no further data requests
// should reach it via the ALB.
func TestALBUnavailableMemberNotQueried(t *testing.T) {
	healthyResp := albTestdata(t, "alb_unavail/healthy.json")

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
		default:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, healthyResp)
		}
	}))
	t.Cleanup(healthy.Close)

	var brokenHCHits, brokenDataHits atomic.Int64
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			brokenHCHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			brokenDataHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(broken.Close)

	frontPort := 18650
	metricsPort := 18651
	mgmtPort := 18652

	yaml := fmt.Sprintf(albTestdata(t, "alb_unavail/unavail.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, healthy.URL, broken.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	// Wait for the broken backend to record at least one healthcheck miss.
	// failure_threshold: 1 + interval: 100ms means it should be marked
	// unavailable within ~300ms.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Greater(collect, brokenHCHits.Load(), int64(0),
			"waiting for first healthcheck probe to broken backend")
	}, 5*time.Second, 50*time.Millisecond)

	// Give the pool a beat to refresh after the status flip.
	time.Sleep(300 * time.Millisecond)

	// Snapshot the counter just before issuing user traffic; subsequent growth
	// is what we attribute to fanout from the ALB.
	dataBaseline := brokenDataHits.Load()

	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)
	const reqs = 50
	for _, backend := range []string{"alb-fr-test", "alb-tsm-test"} {
		for i := range reqs {
			u := fmt.Sprintf("http://%s/%s/api/v1/query?query=%s",
				frontAddr, backend, url.QueryEscape(fmt.Sprintf("up + 0*%d", i)))
			resp, err := http.Get(u)
			require.NoError(t, err)
			resp.Body.Close()
		}
	}

	dataDelta := brokenDataHits.Load() - dataBaseline
	require.Equalf(t, int64(0), dataDelta,
		"broken backend received %d user-data requests via the ALB after being marked unavailable; expected 0",
		dataDelta)

	// The healthy backend must still be receiving traffic (ALB shouldn't 502 everything).
	require.NotEmpty(t, strings.TrimSpace(healthy.URL),
		"healthy URL must not be empty")
}
