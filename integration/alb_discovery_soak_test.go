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
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestALBDiscoverySoak (plan step 37) runs continuous membership churn
// under sustained request load for a configurable duration and asserts no
// goroutine or file-descriptor growth, sampling the process's own
// go_goroutines and process_open_fds metrics. It only runs when
// TRICKSTER_SOAK_TEST=1 (the nightly workflow sets it, with
// TRICKSTER_SOAK_DURATION=30m); it is skipped in regular CI and local
// runs. This soak also serves as the baseline for the gateway controller's
// endpoint-mode soak.
func TestALBDiscoverySoak(t *testing.T) {
	if os.Getenv("TRICKSTER_SOAK_TEST") != "1" {
		t.Skip("soak runs only with TRICKSTER_SOAK_TEST=1")
	}
	duration := 10 * time.Minute
	if d := os.Getenv("TRICKSTER_SOAK_DURATION"); d != "" {
		parsed, err := time.ParseDuration(d)
		require.NoError(t, err, "invalid TRICKSTER_SOAK_DURATION")
		duration = parsed
	}

	const (
		frontPort   = 19550
		metricsPort = 19551
		mgmtPort    = 19552
	)
	leaves := make([]*discoveryLeaf, 6)
	for i := range leaves {
		leaves[i] = newDiscoveryLeaf(t, fmt.Sprintf("leaf%d", i))
	}
	membersPath := t.TempDir() + "/members.yaml"
	writeMembersFile(t, membersPath, leaves[0], leaves[1], leaves[2])

	cfg := discoveryALBConfig(frontPort, metricsPort, mgmtPort,
		"  d1:\n    provider: file",
		"          path: "+membersPath)
	startDiscoveryTrickster(t, cfg)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 3)

	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	stop := make(chan struct{})

	// sustained request load
	for range 2 {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := http.Get(frontURL)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}()
	}
	// continuous membership churn: rotating window of 3 members
	go func() {
		i := 0
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				writeMembersFile(t, membersPath,
					leaves[i%len(leaves)],
					leaves[(i+1)%len(leaves)],
					leaves[(i+2)%len(leaves)])
				i++
			}
		}
	}()
	defer close(stop)

	// sample resource gauges throughout; compare the medians of the first
	// and last quarters so startup transients and momentary spikes don't
	// skew the verdict
	type sample struct{ goroutines, fds float64 }
	var samples []sample
	sampleEvery := max(duration/60, time.Second)
	deadline := time.Now().Add(duration)
	// settle before the first sample
	time.Sleep(min(10*time.Second, duration/10))
	for time.Now().Before(deadline) {
		g, ok1 := metricValue(t, metricsAddr, "go_goroutines", "")
		f, ok2 := metricValue(t, metricsAddr, "process_open_fds", "")
		if ok1 {
			s := sample{goroutines: g}
			if ok2 {
				s.fds = f
			}
			samples = append(samples, s)
		}
		time.Sleep(sampleEvery)
	}
	require.GreaterOrEqual(t, len(samples), 8,
		"not enough samples for a verdict")

	median := func(vals []float64) float64 {
		sort.Float64s(vals)
		return vals[len(vals)/2]
	}
	quarter := len(samples) / 4
	var firstG, lastG, firstF, lastF []float64
	for i, s := range samples {
		switch {
		case i < quarter:
			firstG = append(firstG, s.goroutines)
			firstF = append(firstF, s.fds)
		case i >= len(samples)-quarter:
			lastG = append(lastG, s.goroutines)
			lastF = append(lastF, s.fds)
		}
	}
	gGrowth := median(lastG) - median(firstG)
	fGrowth := median(lastF) - median(firstF)
	t.Logf("soak %s: samples=%d goroutine growth=%.0f fd growth=%.0f",
		duration, len(samples), gGrowth, fGrowth)

	require.LessOrEqual(t, gGrowth, 25.0,
		"goroutine count grew across the soak")
	if median(firstF) > 0 { // process_open_fds is unavailable on some platforms
		require.LessOrEqual(t, fGrowth, 15.0,
			"open file descriptors grew across the soak")
	}

	// membership is still converging at the end of the soak
	writeMembersFile(t, membersPath, leaves[0])
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 1)
}
