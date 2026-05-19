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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// panicOnceTransport panics on its first RoundTrip call and returns a 200 OK
// on subsequent calls. Used to validate that a panic in probe() does not
// kill the per-target probe ticker goroutine.
type panicOnceTransport struct {
	calls atomic.Int32
}

func (p *panicOnceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := p.calls.Add(1)
	if n == 1 {
		panic("simulated probe panic")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// TestProbeLoopSurvivesPanic ensures a panic inside t.probe does not kill
// the probe ticker goroutine, freezing target status at its initial value.
// Without recover, the goroutine dies on the first probe and Status stays
// at StatusInitializing forever.
func TestProbeLoopSurvivesPanic(t *testing.T) {
	logger.SetLogger(testLogger)

	tr := &panicOnceTransport{}
	client := &http.Client{Transport: tr}

	u, err := url.Parse("http://probe-panic.invalid/")
	require.NoError(t, err)

	const interval = 50 * time.Millisecond
	tgt, err := newTarget(context.Background(), "panic-target", "panic-target", &ho.Options{
		Verb:              "GET",
		Scheme:            u.Scheme,
		Host:              u.Host,
		Path:              "/",
		Interval:          interval,
		ExpectedCodes:     []int{200},
		FailureThreshold:  1,
		RecoveryThreshold: 1,
	}, client)
	require.NoError(t, err)

	ctx := t.Context()
	tgt.Start(ctx)
	defer tgt.Stop()

	// Wait long enough for: jitter (up to ~1s) + first probe (panics) +
	// several ticker fires (each returns 200, drives status -> Passing).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tr.calls.Load() >= 3 && tgt.status.Get() == StatusPassing {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.GreaterOrEqual(t, tr.calls.Load(), int32(2),
		"probe loop did not survive panic: only %d RoundTrip calls observed", tr.calls.Load())
	require.Equal(t, StatusPassing, tgt.status.Get(),
		"status did not transition to Passing after panic recovery (calls=%d, status=%d)",
		tr.calls.Load(), tgt.status.Get())
}
