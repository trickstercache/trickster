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

package flightsql

import (
	"errors"
	"fmt"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type failingRenderer struct{}

func (failingRenderer) RenderExtent(timeseries.Extent) (string, error) {
	return "", errors.New("render failed")
}

// nativeDeltaRewriteFailureRequest builds a delta request whose extent
// rendering fails, driving the engine's rewrite-failure hook.
func nativeDeltaRewriteFailureRequest(_ *deltaRunner) nativedelta.DeltaRequest[*deltaPayload] {
	plan := &sqlanalyzer.QueryPlan{
		Step:       time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: time.Unix(0, 0), Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: time.Unix(600, 0), Inclusive: false},
		Renderer:   failingRenderer{},
	}
	return nativedelta.DeltaRequest[*deltaPayload]{
		Key: "k", FallbackKey: "k:fallback", EmptyKey: "k:empty",
		Plan: plan, Now: time.Unix(3600, 0),
		Ops: nativedelta.DeltaOps[*deltaPayload]{
			FetchOriginal: func() (*deltaPayload, error) {
				return &deltaPayload{Raw: []byte{}}, nil
			},
		},
	}
}

// TestServeObservesCacheOutcomes verifies every statement execution through
// the three-tier path records exactly one native SQL cache outcome under the
// flightsql dialect.
func TestServeObservesCacheOutcomes(t *testing.T) {
	up := &fakeUpstream{executeFn: rangedUpstream(t), ipcBytes: buildTestIPC(t)}
	inner := newMemCache()
	const backend = "flight-metrics-test"
	srv := NewServer(up, inner,
		WithCacheKeyPrefix(backend),
		WithDeltaCache(DeltaConfig{
			Analyzer:    testAnalyzer,
			CacheClient: func() trickstercache.Cache { return deltaTestCache{inner: inner} },
			CacheTTL:    time.Hour,
		}))

	counter := func(mode sqlanalyzer.CacheMode, lookupStatus cachestatus.LookupStatus) float64 {
		return testutil.ToFloat64(metrics.SQLQueryCache.WithLabelValues(
			backend, flightsqlDialect, mode.String(), lookupStatus.String()))
	}

	deltaStatement := fmt.Sprintf(deltaQuery, 0, 600)
	executeRows(t, srv, deltaStatement)
	if got := counter(sqlanalyzer.CacheModeDelta, cachestatus.LookupStatusKeyMiss); got != 1 {
		t.Fatalf("delta kmiss = %v, want 1", got)
	}
	executeRows(t, srv, deltaStatement)
	if got := counter(sqlanalyzer.CacheModeDelta, cachestatus.LookupStatusHit); got != 1 {
		t.Fatalf("delta hit = %v, want 1", got)
	}
	points := testutil.ToFloat64(metrics.ProxyRequestElements.WithLabelValues(
		backend, flightsqlDialect, cachestatus.LookupStatusHit.String(), metricPathQuery))
	if points != 20 {
		t.Fatalf("delta hit points = %v, want 20", points)
	}

	up.executeFn = nil // the remaining statements serve the canned payload
	executeRows(t, srv, "SELECT * FROM cpu LIMIT 5")
	if got := counter(sqlanalyzer.CacheModeObject, cachestatus.LookupStatusKeyMiss); got != 1 {
		t.Fatalf("object kmiss = %v, want 1", got)
	}

	executeRows(t, srv, "SELECT now(), * FROM cpu")
	if got := counter(sqlanalyzer.CacheModeNone, cachestatus.LookupStatusProxyOnly); got != 1 {
		t.Fatalf("proxy-only = %v, want 1", got)
	}

	up.returnErr = errors.New("origin down")
	if _, _, err := srv.DoGetStatement(t.Context(),
		fakeStatementTicket{handle: []byte("SELECT now(), * FROM cpu")}); err == nil {
		t.Fatal("upstream failure was swallowed")
	}
	up.returnErr = nil
	if got := counter(sqlanalyzer.CacheModeNone, cachestatus.LookupStatusProxyError); got != 1 {
		t.Fatalf("proxy-error = %v, want 1", got)
	}
}

// TestDeltaEngineFailureHooksAreWired verifies the engine's rewrite-failure
// hook lands on the shared SQL rewrite metric.
func TestDeltaEngineFailureHooksAreWired(t *testing.T) {
	const backend = "flight-rewrite-metrics"
	runner := newDeltaRunner(DeltaConfig{
		Analyzer:    testAnalyzer,
		CacheClient: func() trickstercache.Cache { return nil },
	}, backend)
	rewrites := metrics.SQLQueryRewriteFailures.WithLabelValues(
		backend, flightsqlDialect, "render_extent")
	before := testutil.ToFloat64(rewrites)
	runner.engine.ExecuteDelta(nativeDeltaRewriteFailureRequest(runner))
	if got := testutil.ToFloat64(rewrites); got != before+1 {
		t.Fatalf("rewrite failures = %v, want %v", got, before+1)
	}
}
