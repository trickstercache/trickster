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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIngressEndpointModeKind is the phase 4 Kubernetes controller scenario:
// an in-cluster Trickster (deployed by `make kind-integration-ingress`; see
// kind/README.md) serves an Ingress in the endpoint routing mode, so its
// generated ALB discovers the webecho Service's EndpointSlices. The test
// drives a rolling restart of webecho under sustained load through the
// Ingress and asserts zero client errors: terminating endpoints leave the
// pool before their pods stop, and new ones join only once ready.
//
// Gated on TRICKSTER_KIND_TEST=1 like TestALBDiscoveryKind, and run after it
// by the integration-kind CI job.
func TestIngressEndpointModeKind(t *testing.T) {
	if os.Getenv("TRICKSTER_KIND_TEST") != "1" {
		t.Skip("kind scenario runs only with TRICKSTER_KIND_TEST=1")
	}

	const (
		frontAddr   = "127.0.0.1:30083"
		metricsAddr = "127.0.0.1:30084"
		namespace   = "trickster-it"
		host        = "endpoint.example.com"
		// generatedALB is the ALB the controller generates for the Ingress's
		// one rule; endpoint mode binds its pool to the discovered endpoints
		generatedALB = "kgw--ingress.trickster-it.shop-endpoint_r0"
	)
	frontURL := "http://" + frontAddr + "/hello"

	kubectl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("kubectl", append([]string{
			"--context", "kind-trickster-it", "-n", namespace,
		}, args...)...).
			CombinedOutput()
		require.NoError(t, err, "kubectl %v: %s", args, out)
		return string(out)
	}
	get := func() (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, frontURL, nil)
		if err != nil {
			return nil, err
		}
		req.Host = host
		return discoveryHTTPClient.Do(req)
	}

	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, generatedALB, 2, 2*time.Minute)

	// the route is served through the discovered pool, and a host the
	// Ingress does not claim is not
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := get()
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		assert.Equal(collect, http.StatusOK, resp.StatusCode)
	}, 30*time.Second, 100*time.Millisecond)
	req, err := http.NewRequest(http.MethodGet, frontURL, nil)
	require.NoError(t, err)
	req.Host = "unclaimed.example.com"
	resp, err := discoveryHTTPClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// rolling restart under sustained load through the Ingress
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var requests, errors atomic.Int64
	failures := newRequestFailures()
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
				resp, err := get()
				requests.Add(1)
				if err != nil {
					errors.Add(1)
					failures.transport(err)
					continue
				}
				if resp.StatusCode != http.StatusOK {
					errors.Add(1)
					failures.response(resp)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	kubectl("rollout", "restart", "deployment/webecho")
	restarted := time.Since(failures.start)
	kubectl("rollout", "status", "deployment/webecho", "--timeout=120s")
	rolledOut := time.Since(failures.start)
	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
	t.Logf("endpoint-mode rolling restart: requests=%d errors=%d (restart issued +%dms, rolled out +%dms); failures: %s",
		requests.Load(), errors.Load(), restarted.Milliseconds(),
		rolledOut.Milliseconds(), failures)
	require.Positive(t, requests.Load())
	require.Zero(t, errors.Load(),
		"a rolling restart through an endpoint-mode route must produce zero client errors; failures: %s",
		failures)

	// membership follows the EndpointSlices after the restart as before it
	waitDiscoveredMembers(t, metricsAddr, generatedALB, 2, time.Minute)
	kubectl("scale", "deployment/webecho", "--replicas=3")
	waitDiscoveredMembers(t, metricsAddr, generatedALB, 3, time.Minute)
	kubectl("scale", "deployment/webecho", fmt.Sprintf("--replicas=%d", 2))
	waitDiscoveredMembers(t, metricsAddr, generatedALB, 2, time.Minute)
}
