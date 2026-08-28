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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestALBDiscoveryReloadDuringChurn reloads configuration while discovered
// membership is changing (plan step 36): a churn goroutine rewrites the
// member-list file continuously, SIGHUP-driven config reloads fire
// mid-churn, and request load runs throughout. Asserts no client errors
// (no listener disruption), convergence to the final membership (no lost
// updates), and exactly one health registration per final member (no
// double registration).
func TestALBDiscoveryReloadDuringChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("churn test takes ~15s; skipped under -short")
	}
	// mirror reload_storm_test: the daemon's signal handler must be the
	// only SIGHUP receiver
	signal.Reset(syscall.SIGHUP)

	const (
		frontPort   = 19540
		metricsPort = 19541
		mgmtPort    = 19542
	)
	leaves := make([]*discoveryLeaf, 4)
	for i := range leaves {
		leaves[i] = newDiscoveryLeaf(t, fmt.Sprintf("leaf%d", i))
	}

	membersPath := t.TempDir() + "/members.yaml"
	writeMembersFile(t, membersPath, leaves[0], leaves[1])

	cfg := discoveryALBConfig(frontPort, metricsPort, mgmtPort,
		"  d1:\n    provider: file",
		"          path: "+membersPath)
	cfgPath := startDiscoveryTrickster(t, cfg)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	const (
		churnDuration = 8 * time.Second
		reloadCount   = 6
		workers       = 4
		// full-membership replacement every 250ms is aggressive churn for
		// an 8s window (30+ membership changes across 6 reloads) while
		// staying below loopback ephemeral-port/TIME_WAIT exhaustion,
		// which manifests as client- and upstream-side dial failures that
		// have nothing to do with reload correctness
		churnTick = 250 * time.Millisecond
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var requests, errors atomic.Int64
	var errMtx sync.Mutex
	errSamples := map[string]int{}
	recordErr := func(desc string) {
		errors.Add(1)
		errMtx.Lock()
		errSamples[desc]++
		errMtx.Unlock()
	}

	// sustained request load; every response must be a 200 from a leaf
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := http.Get(frontURL)
				requests.Add(1)
				if err != nil {
					recordErr(err.Error())
					continue
				}
				if resp.StatusCode != http.StatusOK {
					recordErr(fmt.Sprintf("status %d", resp.StatusCode))
				}
				// drain so keep-alive connections are reused; without
				// this the storm exhausts client-side ephemeral ports
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}

	// membership churn: rotate through overlapping member sets
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		ticker := time.NewTicker(churnTick)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				a, b := leaves[i%len(leaves)], leaves[(i+1)%len(leaves)]
				writeMembersFile(t, membersPath, a, b)
				i++
			}
		}
	}()

	// config reloads mid-churn: rewrite the config (touch forces a
	// staleness hit) and SIGHUP
	for i := range reloadCount {
		time.Sleep(churnDuration / time.Duration(reloadCount+1))
		touched := cfg + fmt.Sprintf("\n# reload %d\n", i)
		tmp := cfgPath + ".tmp"
		require.NoError(t, os.WriteFile(tmp, []byte(touched), 0o644))
		require.NoError(t, os.Rename(tmp, cfgPath))
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	}
	time.Sleep(churnDuration / time.Duration(reloadCount+1))
	close(stop)
	wg.Wait()

	t.Logf("requests=%d errors=%d samples=%v", requests.Load(), errors.Load(), errSamples)
	require.Positive(t, requests.Load())
	require.Zero(t, errors.Load(),
		"reload during churn must not disrupt request service")

	// settle on a final membership; no lost updates
	writeMembersFile(t, membersPath, leaves[2], leaves[3])
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	// no double registration and no lost updates: the health page lists
	// each final member exactly once as a standalone status entry (each
	// name also legitimately appears inside the ALB's pool-member detail),
	// and the churned-away members are gone entirely
	statusLines := func(body, member string) int {
		var n int
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, member+" ") {
				n++
			}
		}
		return n
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		body, _ := checkTrickster(t, metricsAddr, "trickster/health", http.StatusOK)
		assert.Equal(collect, 1,
			statusLines(body, "disco-alb-leaf2"), "leaf2 status entries")
		assert.Equal(collect, 1,
			statusLines(body, "disco-alb-leaf3"), "leaf3 status entries")
		assert.NotContains(collect, body, "disco-alb-leaf0")
		assert.NotContains(collect, body, "disco-alb-leaf1")
	}, 15*time.Second, 250*time.Millisecond)
}
