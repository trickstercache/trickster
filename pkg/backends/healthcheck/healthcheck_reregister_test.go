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

package healthcheck

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// Register on an existing name fires `go t2.Stop()` then immediately starts
// a new probe loop. Architecturally, the old probe loop keeps running until
// the async Stop returns, so the two loops can have probes in flight against
// the same upstream simultaneously, and a re-register burst can trigger more
// probes per unit time than the configured interval allows.
//
// This test re-registers repeatedly while the upstream is slow (handler
// ignores cancellation), then asserts the upstream sees no two probes whose
// in-handler lifetimes overlap.
func TestHealthcheckReregisterNoOverlap(t *testing.T) {
	logger.SetLogger(testLogger)
	synctest.Test(t, func(t *testing.T) {
		// handlerDelay must exceed the max randomJitter (1s) used by probeLoop so
		// the in-flight pre-reregister probe is still occupying the upstream when
		// an asynchronously-stopped old loop and a new loop could overlap.
		const interval = 50 * time.Millisecond
		const handlerDelay = 1500 * time.Millisecond

		type probe struct {
			start, end time.Time
		}
		var (
			mu     sync.Mutex
			probes []probe
		)
		first := make(chan struct{}, 1)

		client := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				start := time.Now()
				mu.Lock()
				idx := len(probes)
				probes = append(probes, probe{start: start})
				mu.Unlock()
				if idx == 0 {
					select {
					case first <- struct{}{}:
					default:
					}
				}

				// Ignore cancellation so the upstream sees the full overlap window.
				time.Sleep(handlerDelay)
				end := time.Now()
				mu.Lock()
				probes[idx].end = end
				mu.Unlock()

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("OK")),
					Header:     http.Header{},
					Request:    req,
				}, nil
			}),
		}

		hc := New()
		defer hc.Shutdown()

		mkOpts := func() *ho.Options {
			return &ho.Options{
				Verb:          "GET",
				Scheme:        "http",
				Host:          "healthcheck-reregister.invalid",
				Path:          "/",
				Interval:      interval,
				ExpectedCodes: []int{http.StatusOK},
			}
		}

		_, err := hc.Register("x", "x", mkOpts(), client)
		require.NoError(t, err)

		<-first

		boundary := time.Now()

		_, err = hc.Register("x", "x", mkOpts(), client)
		require.NoError(t, err)
		// Allow new loop's jitter (10ms-1s) plus one handlerDelay window to land.
		time.Sleep(handlerDelay + time.Second + interval)
		synctest.Wait()

		mu.Lock()
		snapshot := make([]probe, 0, len(probes))
		for _, p := range probes {
			if !p.end.IsZero() {
				snapshot = append(snapshot, p)
			}
		}
		mu.Unlock()
		sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].start.Before(snapshot[j].start) })

		// Include in-flight probes whose lifetimes touched the post-reregister
		// window so a leaked old loop's probe and the new loop's first probe
		// would both appear here.
		post := make([]probe, 0, len(snapshot))
		for _, p := range snapshot {
			if p.end.After(boundary) {
				post = append(post, p)
			}
		}

		for i := 0; i < len(post); i++ {
			for j := i + 1; j < len(post); j++ {
				if post[j].start.Before(post[i].end) {
					offsets := make([][2]time.Duration, len(post))
					for k, p := range post {
						offsets[k] = [2]time.Duration{p.start.Sub(boundary), p.end.Sub(boundary)}
					}
					t.Fatalf("overlapping probes: probe[%d] (%v..%v) overlaps probe[%d] (%v..%v); all probes: %v",
						i, post[i].start.Sub(boundary), post[i].end.Sub(boundary),
						j, post[j].start.Sub(boundary), post[j].end.Sub(boundary), offsets)
				}
			}
		}
	})
}
