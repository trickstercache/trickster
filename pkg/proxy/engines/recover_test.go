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

package engines

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if m.Counter == nil || m.Counter.Value == nil {
		return 0
	}
	return *m.Counter.Value
}

// TestGoWithRecoverAsyncPanic asserts the goroutine helper recovers panics
// spawned in fire-and-forget goroutines (the actual shape used at every call
// site in this package). We can't close a sync channel from inside the
// goroutine to wait for completion -- the metric increment happens in the
// outer recover after the panicking fn fully unwinds, so any inner
// defer-signal would race with the counter write. Poll the counter directly
// instead.
func TestGoWithRecoverAsyncPanic(t *testing.T) {
	t.Parallel()

	before := counterValue(t, metrics.ProxyEnginesPanicRecovered, "test.async")

	goWithRecover("test.async", func() {
		panic("simulated async panic")
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := counterValue(t, metrics.ProxyEnginesPanicRecovered, "test.async") - before; got == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected ProxyEnginesPanicRecovered{site=test.async} +1 within 2s, got +%v",
		counterValue(t, metrics.ProxyEnginesPanicRecovered, "test.async")-before)
}

// TestPCFCopyDefersRunOnPanic exercises the ordering invariant that motivated
// moving reqs.Delete into a defer at the objectproxycache PCF copy site: a
// panic mid-copy must not leave a stale reqs map entry. We reproduce the
// closure shape locally rather than driving objectproxycache end-to-end,
// because PrepareFetchReader needs a configured backend; the goroutine body
// shape is what matters.
func TestPCFCopyDefersRunOnPanic(t *testing.T) {
	t.Parallel()

	var reqs sync.Map
	const key = "panic-key"
	reqs.Store(key, struct{}{})

	var closed atomic.Bool
	done := make(chan struct{})

	goWithRecover("test.pcf.copy", func() {
		defer close(done)
		defer reqs.Delete(key)
		defer func() { closed.Store(true) }()
		panic("io.Copy blew up")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recovered goroutine never completed")
	}

	if !closed.Load() {
		t.Fatal("inner defer (pcf.Close stand-in) did not run after panic")
	}
	if _, ok := reqs.Load(key); ok {
		t.Fatal("reqs.Delete(key) did not run after panic; map leak")
	}
}
