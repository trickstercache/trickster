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
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestFailures classifies load-loop failures so that an assertion on a
// zero-error rollout says what actually broke: transport errors by their
// normalized message, and bad responses by status code plus Trickster's
// result header. Offsets are milliseconds since the load started.
type requestFailures struct {
	mtx         sync.Mutex
	byClass     map[string]int
	first, last time.Duration
	start       time.Time
}

// addrRE strips the dial/read target from a transport error so that the
// same failure mode counts as one class regardless of ephemeral port
var addrRE = regexp.MustCompile(`\b(tcp|udp) [0-9a-fA-F.:\[\]]+(->[0-9a-fA-F.:\[\]]+)?: `)

func newRequestFailures() *requestFailures {
	return &requestFailures{byClass: map[string]int{}, start: time.Now()}
}

func (f *requestFailures) record(class string) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	f.byClass[class]++
	at := time.Since(f.start)
	if f.first == 0 || at < f.first {
		f.first = at
	}
	if at > f.last {
		f.last = at
	}
}

func (f *requestFailures) transport(err error) {
	f.record("transport: " + addrRE.ReplaceAllString(err.Error(), ""))
}

func (f *requestFailures) response(resp *http.Response) {
	class := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if r := resp.Header.Get("X-Trickster-Result"); r != "" {
		class += " (" + r + ")"
	}
	f.record(class)
}

func (f *requestFailures) String() string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if len(f.byClass) == 0 {
		return "none"
	}
	classes := make([]string, 0, len(f.byClass))
	for c := range f.byClass {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	var b strings.Builder
	fmt.Fprintf(&b, "first at +%dms, last at +%dms:",
		f.first.Milliseconds(), f.last.Milliseconds())
	for _, c := range classes {
		fmt.Fprintf(&b, "\n  %6d  %s", f.byClass[c], c)
	}
	return b.String()
}

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
				resp, err := discoveryHTTPClient.Get(frontURL)
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
	// keep load flowing while the post-restart endpoints settle
	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
	t.Logf("rolling restart: requests=%d errors=%d (restart issued +%dms, rolled out +%dms); failures: %s",
		requests.Load(), errors.Load(), restarted.Milliseconds(),
		rolledOut.Milliseconds(), failures)
	require.Positive(t, requests.Load())
	require.Zero(t, errors.Load(),
		"rolling restart under load must produce zero client errors; failures: %s",
		failures)

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
