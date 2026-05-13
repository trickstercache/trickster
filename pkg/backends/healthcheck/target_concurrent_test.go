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
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// target.cancel is written in Start and read in Stop without a lock.
// Concurrent Start/Stop should fail under -race.
func TestTargetConcurrentStartStopRace(t *testing.T) {
	logger.SetLogger(testLogger)
	ts := newTestServer(200, "OK", map[string]string{})
	defer ts.Close()
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)

	tgt, err := newTarget(context.Background(), "race", "race", &ho.Options{
		Verb:          "GET",
		Scheme:        u.Scheme,
		Host:          u.Host,
		Path:          "/",
		Interval:      50 * time.Millisecond,
		ExpectedCodes: []int{200},
	}, ts.Client())
	require.NoError(t, err)

	const goroutines = 5
	const cycles = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for range cycles {
				tgt.Start(ctx)
				tgt.Stop()
			}
		}()
	}
	wg.Wait()
}

// target.wg is reused across Start/Stop cycles. If the previous probe
// goroutine hasn't fully exited before the next Start's wg.Go,
// sync.WaitGroup panics with "WaitGroup is reused before previous Wait
// has returned".
func TestTargetRestartAfterStop(t *testing.T) {
	logger.SetLogger(testLogger)
	ts := newTestServer(200, "OK", map[string]string{})
	defer ts.Close()
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)

	tgt, err := newTarget(context.Background(), "restart", "restart", &ho.Options{
		Verb:          "GET",
		Scheme:        u.Scheme,
		Host:          u.Host,
		Path:          "/",
		Interval:      200 * time.Millisecond,
		ExpectedCodes: []int{200},
	}, ts.Client())
	require.NoError(t, err)

	ctx := context.Background()
	for range 10 {
		tgt.Start(ctx)
		time.Sleep(time.Millisecond)
		tgt.Stop()
	}
}
