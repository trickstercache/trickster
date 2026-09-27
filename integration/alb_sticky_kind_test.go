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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestALBStickyDrainingKind(t *testing.T) {
	skipUnlessKind(t)
	const (
		frontAddr   = "127.0.0.1:30080"
		metricsAddr = "127.0.0.1:30081"
		stickyHost  = "sticky.trickster-it"
		stickyALB   = "sticky-alb"
		cookieName  = "trickster_sticky"
	)
	waitForTrickster(t, metricsAddr)
	kubectlKind(t, "", "scale", "deployment/webecho", "--replicas=2")
	kubectlKind(t, "", "rollout", "status", "deployment/webecho", "--timeout=120s")
	waitDiscoveredMembers(t, metricsAddr, stickyALB, 2, time.Minute)

	var cookie atomic.Pointer[http.Cookie]
	get := func(session bool) (*http.Response, error) {
		var h http.Header
		if c := cookie.Load(); session && c != nil {
			h = http.Header{"Cookie": {c.Name + "=" + c.Value}}
		}
		req, err := hostRequest(frontAddr, stickyHost, "/", h)
		if err != nil {
			return nil, err
		}
		return hostClient.Do(req)
	}
	// served reads the name of the pod that answered, which whoami reports as its hostname
	served := func(resp *http.Response) (string, error) {
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("status %d", resp.StatusCode)
		}
		for line := range strings.Lines(string(b)) {
			if h, ok := strings.CutPrefix(strings.TrimSpace(line), "Hostname: "); ok {
				return h, nil
			}
		}
		return "", errors.New("no hostname in the response")
	}
	// keep follows a reissued cookie, as a browser does
	keep := func(resp *http.Response) {
		for _, c := range resp.Cookies() {
			if c.Name == cookieName {
				cookie.Store(c)
			}
		}
	}

	resp, err := get(true)
	require.NoError(t, err)
	keep(resp)
	pinned, err := served(resp)
	require.NoError(t, err)
	require.NotNil(t, cookie.Load(), "the sticky ALB issued no cookie")
	for range 5 {
		resp, err := get(true)
		require.NoError(t, err)
		h, err := served(resp)
		require.NoError(t, err)
		require.Equal(t, pinned, h, "the session left its pod before the rollout")
	}
	member := stickyALB + "-" + pinned

	var (
		draining, stopped                  atomic.Bool
		pinnedWhileDraining, newOnDraining atomic.Int64
		newRequests, newErrors             atomic.Int64
		movedTo                            atomic.Pointer[string]
		wg                                 sync.WaitGroup
	)
	failures := newRequestFailures()
	// the pinned client; errors in the moment its pod stops serving are expected
	wg.Go(func() {
		for !stopped.Load() {
			d := draining.Load()
			resp, err := get(true)
			if err != nil {
				continue
			}
			keep(resp)
			if h, err := served(resp); err == nil {
				if h == pinned {
					if d {
						pinnedWhileDraining.Add(1)
					}
				} else {
					movedTo.Store(&h)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	// new sessions: they never reach the draining pod, and never fail
	for range 3 {
		wg.Go(func() {
			for !stopped.Load() {
				d := draining.Load()
				resp, err := get(false)
				newRequests.Add(1)
				if err != nil {
					newErrors.Add(1)
					failures.transport(err)
					continue
				}
				if resp.StatusCode != http.StatusOK {
					failures.response(resp)
				}
				h, err := served(resp)
				if err != nil {
					newErrors.Add(1)
					continue
				}
				if d && h == pinned {
					newOnDraining.Add(1)
				}
			}
		})
	}
	t.Cleanup(func() {
		stopped.Store(true)
		wg.Wait()
	})

	kubectlKind(t, "", "rollout", "restart", "deployment/webecho")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		v, ok := metricValue(t, metricsAddr, "trickster_alb_member_draining",
			`member="`+member+`"`)
		assert.True(collect, ok && v == 1, "%s is not draining", member)
	}, 2*time.Minute, 50*time.Millisecond)
	draining.Store(true)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := discoveryHTTPClient.Get("http://" + metricsAddr + "/trickster/health?json")
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		var page struct {
			Available []struct {
				Name     string   `json:"name"`
				Draining []string `json:"drainingPoolMembers"`
			} `json:"available"`
		}
		if !assert.NoError(collect, json.NewDecoder(resp.Body).Decode(&page)) {
			return
		}
		var draining []string
		for _, b := range page.Available {
			if b.Name == stickyALB {
				draining = b.Draining
			}
		}
		assert.Contains(collect, draining, member)
	}, 3*time.Second, 50*time.Millisecond, "the health page does not show %s draining", member)

	kubectlKind(t, "", "rollout", "status", "deployment/webecho", "--timeout=120s")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.NotNil(collect, movedTo.Load(), "the session never left its terminated pod")
	}, time.Minute, 100*time.Millisecond)
	waitDiscoveredMembers(t, metricsAddr, stickyALB, 2, time.Minute)

	stopped.Store(true)
	wg.Wait()
	t.Logf("pinned pod %s: %d pinned requests while draining; session moved to %s; "+
		"new sessions: requests=%d errors=%d, failures: %s", pinned, pinnedWhileDraining.Load(),
		*movedTo.Load(), newRequests.Load(), newErrors.Load(), failures)
	require.Positive(t, pinnedWhileDraining.Load(), "the draining pod lost its pinned session")
	require.Zero(t, newOnDraining.Load(), "a new session reached the draining pod")
	require.Positive(t, newRequests.Load())
	require.Zero(t, newErrors.Load(), "new sessions failed during the rollout; failures: %s", failures)
	_, found := metricValue(t, metricsAddr, "trickster_alb_member_draining", `member="`+member+`"`)
	require.False(t, found, "a member that left the pool is still reported draining")
}
