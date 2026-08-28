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
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestALBDiscoveryKind is the plan step-34 Kubernetes scenario: an
// in-cluster Trickster (deployed by `make kind-integration-start`; see
// kind/README.md) discovers its ALB pool from the webecho Service's
// EndpointSlices. The test scales the target Deployment up and down,
// performs a rolling restart under sustained load asserting zero client
// errors, and pauses the kind control-plane node's container to sever the
// API connection, asserting the last-good pool keeps serving. The cluster
// has a separate worker node hosting the workloads (and the host port
// mappings), so pausing the control plane freezes only the API, not the
// data plane.
//
// Gated on TRICKSTER_KIND_TEST=1: it requires the kind cluster, kubectl,
// and docker on the host, and is run by the integration-kind CI job.
func TestALBDiscoveryKind(t *testing.T) {
	if os.Getenv("TRICKSTER_KIND_TEST") != "1" {
		t.Skip("kind scenario runs only with TRICKSTER_KIND_TEST=1")
	}

	const (
		frontAddr   = "127.0.0.1:30080"
		metricsAddr = "127.0.0.1:30081"
		namespace   = "trickster-it"
		cpContainer = "trickster-it-control-plane"
	)
	frontURL := "http://" + frontAddr + "/"

	kubectl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("kubectl", append([]string{
			"--context", "kind-trickster-it", "-n", namespace}, args...)...).
			CombinedOutput()
		require.NoError(t, err, "kubectl %v: %s", args, out)
		return string(out)
	}
	scaleWebecho := func(replicas int) {
		kubectl("scale", "deployment/webecho",
			fmt.Sprintf("--replicas=%d", replicas))
		waitDiscoveredMembers(t, metricsAddr, "disco-alb", float64(replicas))
	}

	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	// traffic reaches distinct discovered pods (whoami reports its
	// hostname in the response body)
	hostnames := map[string]struct{}{}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		status, body := getBody(t, frontURL)
		if !assert.Equal(collect, http.StatusOK, status) {
			return
		}
		for _, line := range strings.Split(body, "\n") {
			if h, ok := strings.CutPrefix(line, "Hostname: "); ok {
				hostnames[h] = struct{}{}
			}
		}
		assert.GreaterOrEqual(collect, len(hostnames), 2,
			"round robin should reach both pods")
	}, 30*time.Second, 100*time.Millisecond)

	// scale up and down: membership follows the EndpointSlices
	scaleWebecho(4)
	scaleWebecho(2)

	// rolling restart under sustained load: terminating endpoints drain
	// out before their pods die, so clients see zero errors
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var requests, errors atomic.Int64
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := discoveryHTTPClient.Get(frontURL)
				requests.Add(1)
				if err != nil {
					errors.Add(1)
					continue
				}
				if resp.StatusCode != http.StatusOK {
					errors.Add(1)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	kubectl("rollout", "restart", "deployment/webecho")
	kubectl("rollout", "status", "deployment/webecho", "--timeout=120s")
	// keep load flowing while the post-restart endpoints settle
	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
	t.Logf("rolling restart: requests=%d errors=%d",
		requests.Load(), errors.Load())
	require.Positive(t, requests.Load())
	require.Zero(t, errors.Load(),
		"rolling restart under load must produce zero client errors")

	// sever the API-server connection: pause the kind control-plane
	// node's container. The workloads live on the worker node, so the
	// data plane keeps running; the last-good pool keeps serving and
	// membership holds.
	require.NoError(t,
		exec.Command("docker", "pause", cpContainer).Run())
	unpaused := false
	defer func() {
		if !unpaused {
			_ = exec.Command("docker", "unpause", cpContainer).Run()
		}
	}()
	for range 20 {
		status, _ := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status,
			"requests must keep succeeding while the API server is unreachable")
		time.Sleep(100 * time.Millisecond)
	}
	members, ok := metricValue(t, metricsAddr,
		"trickster_alb_discovery_members", `alb_name="disco-alb"`)
	require.True(t, ok)
	require.Equal(t, float64(2), members,
		"membership must hold last-good while the API server is unreachable")
	require.NoError(t,
		exec.Command("docker", "unpause", cpContainer).Run())
	unpaused = true

	// after the API returns, discovery converges again. The API server
	// needs a beat to accept connections post-unpause, and Trickster's
	// informer re-establishes its watch on client-go's retry backoff, so
	// these waits get generous windows.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		out, err := exec.Command("kubectl", "--context", "kind-trickster-it",
			"-n", namespace, "get", "deployment", "webecho").CombinedOutput()
		assert.NoError(collect, err, "%s", out)
	}, 60*time.Second, time.Second, "API server did not recover after unpause")
	kubectl("scale", "deployment/webecho", "--replicas=3")
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 3, 3*time.Minute)
	kubectl("scale", "deployment/webecho", "--replicas=2")
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2, time.Minute)
}
