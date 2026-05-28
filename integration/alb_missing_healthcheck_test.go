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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
)

// TestALBHealthyFloorAdmitsFailingMetric verifies the warning surface for an
// ALB whose healthy_floor admits Failing members. An operator who lowered
// healthy_floor below 0 to keep traffic flowing during the Initializing
// startup window also admits members the probe has confirmed broken; the
// `trickster_alb_pool_admits_failing` gauge surfaces that misconfiguration.
func TestALBHealthyFloorAdmitsFailingMetric(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
	}))
	t.Cleanup(healthy.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)

	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]

	yaml := fmt.Sprintf(albTestdata(t, "alb_missing_hc/floor_warn.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, healthy.URL, broken.URL)
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		lines := checkTricksterMetrics(t, metricsAddr)
		var admits, excludes string
		for _, l := range lines {
			if strings.HasPrefix(l, "trickster_alb_pool_admits_failing{") {
				if strings.Contains(l, `backend_name="alb-admits-failing"`) {
					admits = l
				}
				if strings.Contains(l, `backend_name="alb-excludes-failing"`) {
					excludes = l
				}
			}
		}
		assert.True(collect, strings.HasSuffix(admits, " 1"),
			"alb-admits-failing must report 1: %q", admits)
		assert.True(collect, strings.HasSuffix(excludes, " 0"),
			"alb-excludes-failing must report 0: %q", excludes)
	}, 5*time.Second, 200*time.Millisecond)
}

// TestALBHealthyFloorResetWhenMemberHasNoHealthcheck covers #1015: an ALB with
// healthy_floor: 1 whose only member has no health check. The member is stuck
// Unchecked and would be permanently excluded (empty pool -> 502). Trickster
// resets the effective floor to 0, sets trickster_alb_pool_floor_reset, and
// keeps serving 200.
func TestALBHealthyFloorResetWhenMemberHasNoHealthcheck(t *testing.T) {
	vector := albTestdata(t, "alb_unavail/healthy.json")
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, vector)
	}))
	t.Cleanup(origin.Close)

	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	yaml := fmt.Sprintf(albTestdata(t, "alb_missing_hc/floor_reset.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, origin.URL)
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var line string
		for _, l := range checkTricksterMetrics(t, metricsAddr) {
			if strings.HasPrefix(l, "trickster_alb_pool_floor_reset{") &&
				strings.Contains(l, `backend_name="alb-floor1"`) {
				line = l
			}
		}
		assert.True(collect, strings.HasSuffix(line, " 1"),
			"alb-floor1 floor-reset gauge must be 1: %q", line)
	}, 5*time.Second, 200*time.Millisecond)

	// member admitted under the reset floor -> 200, not an empty-pool 502
	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)
	resp, err := http.Get(fmt.Sprintf("http://%s/alb-floor1/api/v1/query?query=up", frontAddr))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestALBPoolDegradeWarnsInResponse covers thinker0's silent single-member
// degrade: a 2-member TSM pool where one member's probe fails drops to one live
// member. TSM still serves 200 from the survivor, but the response must carry a
// `warnings` entry so the caller knows the merge collapsed to a single shard.
func TestALBPoolDegradeWarnsInResponse(t *testing.T) {
	vector := albTestdata(t, "alb_unavail/healthy.json")
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, vector)
	}))
	t.Cleanup(ok.Close)
	var badData atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status/buildinfo" {
			badData.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	yaml := fmt.Sprintf(albTestdata(t, "alb_missing_hc/degrade.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, ok.URL, bad.URL)
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	var n atomic.Int64
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		// unique query per attempt so the cache can't mask the live merge
		q := fmt.Sprintf("up + 0*%d", n.Add(1))
		u := fmt.Sprintf("http://%s/alb-degrade/api/v1/query?query=%s", frontAddr, url.QueryEscape(q))
		resp, err := http.Get(u)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if !assert.Equal(collect, http.StatusOK, resp.StatusCode, "body: %s", b) {
			return
		}
		var pr struct {
			Status   string   `json:"status"`
			Warnings []string `json:"warnings"`
		}
		if !assert.NoError(collect, json.Unmarshal(b, &pr)) {
			return
		}
		var found bool
		for _, wn := range pr.Warnings {
			if strings.Contains(wn, "1 of 2 pool members") {
				found = true
			}
		}
		assert.True(collect, found,
			"expected degrade warning in response warnings: %v", pr.Warnings)
	}, 6*time.Second, 200*time.Millisecond)

	require.Zero(t, badData.Load(), "failing member must not receive data requests")
}
