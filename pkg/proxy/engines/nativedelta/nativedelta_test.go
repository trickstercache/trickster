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

package nativedelta

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// payload is the test protocol representation: a set of rendered statements
// whose results were merged, so tests can assert exactly which fetches
// composed a response.
type payload struct {
	Statements []string
}

type testCodec struct{ marshalErr error }

func (c testCodec) Marshal(p *payload) ([]byte, error) {
	if c.marshalErr != nil {
		return nil, c.marshalErr
	}
	return []byte(strings.Join(p.Statements, "\n")), nil
}

func (testCodec) Unmarshal(data []byte) (*payload, error) {
	if len(data) == 0 {
		return &payload{}, nil
	}
	return &payload{Statements: strings.Split(string(data), "\n")}, nil
}

func (testCodec) Size(p *payload) int {
	size := 0
	for _, statement := range p.Statements {
		size += len(statement)
	}
	return size
}

type testCache struct {
	mtx         sync.Mutex
	data        map[string][]byte
	storeErr    error
	retrieveErr error
	removeErr   error
}

func newTestCache() *testCache { return &testCache{data: make(map[string][]byte)} }

func (c *testCache) Connect() error { return nil }
func (c *testCache) Close() error   { return nil }

func (c *testCache) Store(key string, data []byte, _ time.Duration) error {
	if c.storeErr != nil {
		return c.storeErr
	}
	c.mtx.Lock()
	c.data[key] = append([]byte(nil), data...)
	c.mtx.Unlock()
	return nil
}

func (c *testCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	if c.retrieveErr != nil {
		return nil, status.LookupStatusError, c.retrieveErr
	}
	c.mtx.Lock()
	data, ok := c.data[key]
	c.mtx.Unlock()
	if !ok {
		return nil, status.LookupStatusKeyMiss, trickstercache.ErrKNF
	}
	return append([]byte(nil), data...), status.LookupStatusHit, nil
}

func (c *testCache) Remove(keys ...string) error {
	if c.removeErr != nil {
		return c.removeErr
	}
	c.mtx.Lock()
	for _, key := range keys {
		delete(c.data, key)
	}
	c.mtx.Unlock()
	return nil
}

func (c *testCache) Configuration() *cacheoptions.Options { return cacheoptions.New() }

func newTestEngine(c trickstercache.Cache) *Engine[*payload] {
	return New(Config{
		Protocol: "test", BackendName: "test-backend",
		CacheClient: func() trickstercache.Cache { return c },
		CacheTTL:    time.Minute,
	}, testCodec{})
}

// testRenderer renders extents as "range(start,end)".
type testRenderer struct{ err error }

func (r testRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return fmt.Sprintf("range(%d,%d)", extent.Start.Unix(), extent.End.Unix()), nil
}

func testPlan(lower, upper int64) *sqlanalyzer.QueryPlan {
	return &sqlanalyzer.QueryPlan{
		CanonicalSQL: "canonical",
		Step:         time.Minute,
		LowerBound:   &sqlanalyzer.Bound{Value: time.Unix(lower, 0), Inclusive: true},
		UpperBound:   &sqlanalyzer.Bound{Value: time.Unix(upper, 0), Inclusive: false},
		Renderer:     testRenderer{},
	}
}

// testOps returns DeltaOps whose payloads carry the statements that produced
// them. counts tracks upstream fetches.
func testOps(counts *int) DeltaOps[*payload] {
	return DeltaOps[*payload]{
		Fetch: func(statement string) (*payload, error) {
			*counts++
			return &payload{Statements: []string{statement}}, nil
		},
		FetchOriginal: func() (*payload, error) {
			*counts++
			return &payload{Statements: []string{"original"}}, nil
		},
		Merge: func(parts []*payload) (*payload, error) {
			merged := &payload{}
			for _, part := range parts {
				merged.Statements = append(merged.Statements, part.Statements...)
			}
			return merged, nil
		},
		CropResponse: func(p *payload, _ timeseries.Extent) (*payload, error) {
			return p, nil
		},
		Finalize: func(merged *payload, allExtents timeseries.ExtentList,
			_ timeseries.Extent, _ time.Time,
		) (*payload, *payload, timeseries.ExtentList, error) {
			return merged, merged, allExtents, nil
		},
		ObjectFallback: func() (*payload, status.LookupStatus, error) {
			return &payload{Statements: []string{"object"}}, status.LookupStatusKeyMiss, nil
		},
	}
}

var testNow = time.Unix(2_000_000_000, 0) // every test window has ended by then

func deltaRequest(plan *sqlanalyzer.QueryPlan, ops DeltaOps[*payload]) DeltaRequest[*payload] {
	return DeltaRequest[*payload]{
		Key: "dpc", FallbackKey: "dpc-fallback", EmptyKey: "dpc-empty",
		Plan: plan, Now: testNow, RequireUpperBound: true, Ops: ops,
	}
}

// TestExecuteDeltaObservesRetentionFactor verifies the shared engine reports a
// request spanning more buckets than the backend can retain, which otherwise
// degrades to a partial hit on every request with no other signal.
func TestExecuteDeltaObservesRetentionFactor(t *testing.T) {
	// testPlan uses a 1m step, so 0..600 exclusive is 10 buckets
	const backend = "retention-engine-test"
	newEngine := func(retentionPoints int) *Engine[*payload] {
		return New(Config{
			Protocol: "test", BackendName: backend,
			CacheClient:     func() trickstercache.Cache { return newTestCache() },
			CacheTTL:        time.Minute,
			RetentionPoints: retentionPoints,
		}, testCodec{})
	}
	exceeded := func() float64 {
		return testutil.ToFloat64(
			metrics.TimeseriesRetentionFactorExceeded.WithLabelValues(backend))
	}

	counts := 0
	before := exceeded()
	if _, _, err := newEngine(0).ExecuteDelta(
		deltaRequest(testPlan(0, 600), testOps(&counts))); err != nil {
		t.Fatalf("ExecuteDelta() error = %v", err)
	}
	if got := exceeded(); got != before {
		t.Errorf("unlimited retention recorded %v overages, want none", got-before)
	}

	if _, _, err := newEngine(10).ExecuteDelta(
		deltaRequest(testPlan(0, 600), testOps(&counts))); err != nil {
		t.Fatalf("ExecuteDelta() error = %v", err)
	}
	if got := exceeded(); got != before {
		t.Errorf("a window matching the factor recorded %v overages, want none", got-before)
	}

	if _, _, err := newEngine(9).ExecuteDelta(
		deltaRequest(testPlan(0, 600), testOps(&counts))); err != nil {
		t.Fatalf("ExecuteDelta() error = %v", err)
	}
	if got := exceeded(); got != before+1 {
		t.Errorf("exceeded count = %v, want %v", got, before+1)
	}
}

func TestExecuteDeltaMissThenHitThenPartial(t *testing.T) {
	cacheClient := newTestCache()
	engine := newTestEngine(cacheClient)
	counts := 0
	ops := testOps(&counts)

	// full miss fetches the whole window as one rendered statement
	response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
	if err != nil || cacheStatus != status.LookupStatusKeyMiss || counts != 1 ||
		response.Statements[0] != "range(0,540)" {
		t.Fatalf("full miss = %+v, %s, %v (fetches=%d)", response, cacheStatus, err, counts)
	}

	// identical window is a pure hit with no upstream fetch
	response, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
	if err != nil || cacheStatus != status.LookupStatusHit || counts != 1 {
		t.Fatalf("hit = %+v, %s, %v (fetches=%d)", response, cacheStatus, err, counts)
	}

	// widened window fetches only the missing extent and merges
	response, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 900), ops))
	if err != nil || cacheStatus != status.LookupStatusPartialHit || counts != 2 {
		t.Fatalf("partial = %+v, %s, %v (fetches=%d)", response, cacheStatus, err, counts)
	}
	if len(response.Statements) != 2 || response.Statements[1] != "range(600,840)" {
		t.Fatalf("partial fetch composed %v", response.Statements)
	}

	// a disjoint window is a range miss
	_, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(7200, 7800), ops))
	if err != nil || cacheStatus != status.LookupStatusRangeMiss {
		t.Fatalf("range miss status = %s, %v", cacheStatus, err)
	}
}

func TestExecuteDeltaFallbacks(t *testing.T) {
	t.Run("unsupported bounds proxy the original", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		response, cacheStatus, err := engine.ExecuteDelta(
			deltaRequest(&sqlanalyzer.QueryPlan{}, testOps(&counts)))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			response.Statements[0] != "original" {
			t.Fatalf("bounds fallback = %+v, %s, %v", response, cacheStatus, err)
		}
	})

	t.Run("render failure proxies the original", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		plan := testPlan(0, 600)
		plan.Renderer = testRenderer{err: errors.New("render failed")}
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, testOps(&counts)))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			response.Statements[0] != "original" {
			t.Fatalf("render fallback = %+v, %s, %v", response, cacheStatus, err)
		}
	})

	t.Run("fetch failure is a proxy error", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		ops.Fetch = func(string) (*payload, error) { return nil, errors.New("origin down") }
		_, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err == nil || cacheStatus != status.LookupStatusProxyError {
			t.Fatalf("fetch failure = %s, %v", cacheStatus, err)
		}
	})

	t.Run("unmergeable marks the plan and serves the object path", func(t *testing.T) {
		cacheClient := newTestCache()
		engine := newTestEngine(cacheClient)
		counts := 0
		ops := testOps(&counts)
		ops.Merge = func([]*payload) (*payload, error) {
			return nil, errors.New("unorderable group column")
		}
		response, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || response.Statements[0] != "object" {
			t.Fatalf("unmergeable fallback = %+v, %v", response, err)
		}
		if _, blocked := engine.Retrieve("dpc-fallback"); !blocked {
			t.Fatal("fallback marker was not stored")
		}
		// later requests skip the delta fetch entirely
		fetchesBefore := counts
		response, _, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || response.Statements[0] != "object" || counts != fetchesBefore {
			t.Fatalf("marker did not short-circuit: %+v (fetches %d->%d)",
				response, fetchesBefore, counts)
		}
	})

	t.Run("unmergeable fetch error routes to the object path", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		ops.Fetch = func(string) (*payload, error) {
			return nil, Unmergeable(errors.New("schema not representable"))
		}
		response, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || response.Statements[0] != "object" {
			t.Fatalf("unmergeable fetch fallback = %+v, %v", response, err)
		}
	})

	t.Run("invalid cached axis refetches from scratch", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		cropErr := errors.New("bad axis")
		ops.CropResponse = func(*payload, timeseries.Extent) (*payload, error) {
			return nil, cropErr
		}
		if _, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops)); err != nil {
			t.Fatal(err)
		}
		_, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || cacheStatus != status.LookupStatusKeyMiss || counts != 2 {
			t.Fatalf("invalid axis retry = %s, %v (fetches=%d)", cacheStatus, err, counts)
		}
	})

	t.Run("finalize failure is a proxy error", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		ops.Finalize = func(*payload, timeseries.ExtentList, timeseries.Extent, time.Time,
		) (*payload, *payload, timeseries.ExtentList, error) {
			return nil, nil, nil, errors.New("finalize failed")
		}
		if _, cacheStatus, err := engine.ExecuteDelta(
			deltaRequest(testPlan(0, 600), ops)); err == nil ||
			cacheStatus != status.LookupStatusProxyError {
			t.Fatalf("finalize failure = %s, %v", cacheStatus, err)
		}
	})
}

func TestExecuteDeltaEmptyWindow(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	ops := testOps(&counts)
	plan := testPlan(0, 30) // no complete bucket

	t.Run("no empty renderer proxies the original", func(t *testing.T) {
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, ops))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			response.Statements[0] != "original" {
			t.Fatalf("empty window without renderer = %+v, %s, %v", response, cacheStatus, err)
		}
	})

	t.Run("empty responses are cached whole", func(t *testing.T) {
		ops.RenderEmpty = func(lower, upper time.Time) (string, error) {
			return fmt.Sprintf("empty(%d,%d)", lower.Unix(), upper.Unix()), nil
		}
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, ops))
		if err != nil || cacheStatus != status.LookupStatusKeyMiss ||
			response.Statements[0] != "empty(0,0)" {
			t.Fatalf("empty miss = %+v, %s, %v", response, cacheStatus, err)
		}
		fetches := counts
		_, cacheStatus, err = engine.ExecuteDelta(deltaRequest(plan, ops))
		if err != nil || cacheStatus != status.LookupStatusHit || counts != fetches {
			t.Fatalf("empty hit = %s, %v (fetches=%d)", cacheStatus, err, counts)
		}
	})

	t.Run("empty render failure proxies the original", func(t *testing.T) {
		ops.RenderEmpty = func(time.Time, time.Time) (string, error) {
			return "", errors.New("render failed")
		}
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, ops))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			response.Statements[0] != "original" {
			t.Fatalf("empty render failure = %+v, %s, %v", response, cacheStatus, err)
		}
	})
}

func TestExecuteObject(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	fetch := func() (*payload, error) {
		counts++
		return &payload{Statements: []string{"whole"}}, nil
	}
	response, cacheStatus, err := engine.ExecuteObject("opc", fetch)
	if err != nil || cacheStatus != status.LookupStatusKeyMiss || counts != 1 ||
		response.Statements[0] != "whole" {
		t.Fatalf("object miss = %+v, %s, %v", response, cacheStatus, err)
	}
	_, cacheStatus, err = engine.ExecuteObject("opc", fetch)
	if err != nil || cacheStatus != status.LookupStatusHit || counts != 1 {
		t.Fatalf("object hit = %s, %v (fetches=%d)", cacheStatus, err, counts)
	}
	_, cacheStatus, err = engine.ExecuteObject("opc-err", func() (*payload, error) {
		return nil, errors.New("origin down")
	})
	if err == nil || cacheStatus != status.LookupStatusProxyError {
		t.Fatalf("object error = %s, %v", cacheStatus, err)
	}
}

func TestEnvelopeRoundTripAndCorruption(t *testing.T) {
	engine := newTestEngine(nil)
	entry := &Entry[*payload]{
		Payload: &payload{Statements: []string{"a", "b"}},
		Extents: timeseries.ExtentList{{Start: time.Unix(60, 0), End: time.Unix(120, 0)}},
	}
	data, err := engine.MarshalEntry(entry)
	if err != nil {
		t.Fatal(err)
	}
	got, err := engine.UnmarshalEntry(data)
	if err != nil || got.Marker || len(got.Payload.Statements) != 2 ||
		len(got.Extents) != 1 || !got.Extents[0].Start.Equal(time.Unix(60, 0)) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}

	marker, err := engine.MarshalEntry(&Entry[*payload]{Marker: true})
	if err != nil {
		t.Fatal(err)
	}
	gotMarker, err := engine.UnmarshalEntry(marker)
	if err != nil || !gotMarker.Marker {
		t.Fatalf("marker round trip = %+v, %v", gotMarker, err)
	}

	for name, corrupt := range map[string][]byte{
		"nil":           nil,
		"not an entry":  []byte("not-a-cache-entry"),
		"short":         data[:10],
		"bad version":   append([]byte{'T', 'N', 'D', 'C', 99}, data[5:]...),
		"extent count":  append([]byte(nil), data...),
		"truncated len": data[:len(data)-1],
	} {
		if name == "extent count" {
			corrupt[7], corrupt[8], corrupt[9], corrupt[10] = 0xff, 0xff, 0xff, 0xff
		}
		t.Run(name, func(t *testing.T) {
			if _, err := engine.UnmarshalEntry(corrupt); err == nil {
				t.Fatal("corrupt envelope decoded")
			}
		})
	}
}

func TestLocksOnlySerializeMatchingKeys(t *testing.T) {
	engine := newTestEngine(nil)
	leftLock := engine.lock("left")
	rightDone := make(chan struct{})
	go func() {
		lock := engine.lock("right")
		engine.unlock("right", lock)
		close(rightDone)
	}()
	select {
	case <-rightDone:
	case <-time.After(time.Second):
		engine.unlock("left", leftLock)
		t.Fatal("distinct keys blocked each other")
	}

	sameDone := make(chan struct{})
	go func() {
		lock := engine.lock("left")
		engine.unlock("left", lock)
		close(sameDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		engine.lockMtx.Lock()
		references := leftLock.references
		engine.lockMtx.Unlock()
		if references == 2 {
			break
		}
		if time.Now().After(deadline) {
			engine.unlock("left", leftLock)
			t.Fatal("matching-key waiter did not register")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-sameDone:
		t.Fatal("matching keys did not serialize")
	default:
	}
	engine.unlock("left", leftLock)
	select {
	case <-sameDone:
	case <-time.After(time.Second):
		t.Fatal("matching-key waiter remained blocked")
	}
	engine.lockMtx.Lock()
	remaining := len(engine.locks)
	engine.lockMtx.Unlock()
	if remaining != 0 {
		t.Fatalf("lock registry retained %d entries", remaining)
	}
}

func TestBuildWindowBounds(t *testing.T) {
	now := time.Unix(3600, 0)

	t.Run("closed bounds normalize to cadence", func(t *testing.T) {
		window, err := BuildWindow(testPlan(5, 185), now, true)
		if err != nil {
			t.Fatal(err)
		}
		if !window.Output.Start.Equal(time.Unix(60, 0)) ||
			!window.Output.End.Equal(time.Unix(120, 0)) || window.Empty {
			t.Fatalf("window = %+v", window)
		}
	})

	t.Run("sub-cadence range is empty", func(t *testing.T) {
		window, err := BuildWindow(testPlan(5, 25), now, true)
		if err != nil || !window.Empty || !window.Lower.Equal(time.Unix(60, 0)) {
			t.Fatalf("window = %+v, %v", window, err)
		}
	})

	t.Run("open upper runs to the still-filling bucket when allowed", func(t *testing.T) {
		plan := testPlan(0, 0)
		plan.UpperBound = nil
		if _, err := BuildWindow(plan, now, true); !errors.Is(err, ErrUnsupportedBounds) {
			t.Fatalf("required upper bound accepted an open plan: %v", err)
		}
		for _, now := range []time.Time{now, now.Add(30 * time.Second)} {
			window, err := BuildWindow(plan, now, false)
			if err != nil || !window.Output.End.Equal(time.Unix(3540, 0)) || !window.Upper.Equal(time.Unix(3600, 0)) {
				t.Fatalf("open window at %d = %+v, %v", now.Unix(), window, err)
			}
		}
	})

	t.Run("buckets that have not ended are left out", func(t *testing.T) {
		window, err := BuildWindow(testPlan(3000, 7200), now.Add(30*time.Second), true)
		if err != nil || !window.Output.End.Equal(time.Unix(3540, 0)) {
			t.Fatalf("window = %+v, %v", window, err)
		}
		if window, err = BuildWindow(testPlan(3600, 7200), now.Add(30*time.Second), true); err != nil ||
			!window.Empty {
			t.Fatalf("a window of unfinished buckets = %+v, %v", window, err)
		}
	})

	t.Run("inclusive upper names the final bucket", func(t *testing.T) {
		plan := testPlan(0, 0)
		plan.UpperBound = &sqlanalyzer.Bound{Value: time.Unix(600, 0), Inclusive: true}
		if _, err := BuildWindow(plan, now, true); !errors.Is(err, ErrUnsupportedBounds) {
			t.Fatalf("required exclusive upper accepted inclusive: %v", err)
		}
		window, err := BuildWindow(plan, now, false)
		if err != nil || !window.Output.End.Equal(time.Unix(600, 0)) {
			t.Fatalf("inclusive window = %+v, %v", window, err)
		}
	})

	t.Run("invalid bounds are rejected", func(t *testing.T) {
		invalid := []*sqlanalyzer.QueryPlan{
			nil,
			{},
			{Step: time.Minute},
			{Step: time.Minute, LowerBound: &sqlanalyzer.Bound{Value: now}},
			testPlan(600, 0),
		}
		for i, plan := range invalid {
			if _, err := BuildWindow(plan, now, false); err == nil {
				t.Fatalf("invalid plan %d accepted", i)
			}
		}
	})
}

func TestStableExtentsTrimsVolatileTail(t *testing.T) {
	now := time.Unix(600, 0)
	extents := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(600, 0)}}
	got := StableExtents(extents, time.Minute, 0, 3*time.Minute, now)
	if len(got) != 1 || !got[0].End.Equal(time.Unix(360, 0)) {
		t.Fatalf("stable extents = %v", got)
	}
	// the bucket containing now is still aggregating, so even a zero or negative
	// window removes it, and only it
	for _, window := range []time.Duration{0, -time.Minute} {
		if got := StableExtents(extents, time.Minute, 0, window, now); len(got) != 1 ||
			!got[0].End.Equal(time.Unix(540, 0)) {
			t.Fatalf("window %s kept the live bucket: %v", window, got)
		}
	}
	complete := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(540, 0)}}
	if got := StableExtents(complete, time.Minute, 0, 0, now); len(got) != 1 ||
		!got[0].End.Equal(time.Unix(540, 0)) {
		t.Fatalf("zero window trimmed a complete bucket: %v", got)
	}
	if got := StableExtents(extents, time.Minute, 0, time.Hour, now); len(got) != 0 {
		t.Fatalf("fully volatile extents survived: %v", got)
	}
}

func TestVolatileWindow(t *testing.T) {
	tests := []struct {
		configured, step, requested, expected time.Duration
		points                                int
	}{
		{time.Minute, time.Minute, 0, 3 * time.Minute, 3},
		{5 * time.Minute, time.Minute, 0, 5 * time.Minute, 2},
		{0, time.Minute, 2 * time.Minute, 2 * time.Minute, 0},
		{0, time.Minute, 0, 0, 0},
	}
	for _, test := range tests {
		if got := VolatileWindow(test.configured, test.points, test.step, test.requested); got != test.expected {
			t.Errorf("VolatileWindow(%s, %d, %s, %s) = %s, want %s", test.configured, test.points,
				test.step, test.requested, got, test.expected)
		}
	}
}

func TestStableExtentsKeepsTheGrid(t *testing.T) {
	const day = 24 * time.Hour
	at := func(d, h, m int) time.Time { return time.Date(2024, 1, d, h, m, 0, 0, time.UTC) }
	tests := []struct {
		name        string
		step, phase time.Duration
		extent      timeseries.Extent
		window      time.Duration
		now         time.Time
		expectedEnd time.Time
		expectedGap timeseries.Extent
	}{
		{
			// the 23:30 bucket still holds rows inside the volatile hour, so it is refetched
			name: "phased hourly buckets", step: time.Hour, phase: 30 * time.Minute,
			extent: timeseries.Extent{Start: at(1, 22, 30), End: at(2, 0, 30)},
			window: time.Hour, now: at(2, 1, 0), expectedEnd: at(1, 22, 30),
			expectedGap: timeseries.Extent{Start: at(1, 23, 30), End: at(2, 0, 30)},
		},
		{
			// weekly buckets from the Unix epoch fall on Thursdays
			name: "weekly buckets on the epoch grid", step: 7 * day,
			extent: timeseries.Extent{Start: at(4, 0, 0), End: at(25, 0, 0)},
			window: day, now: at(26, 12, 0), expectedEnd: at(18, 0, 0),
			expectedGap: timeseries.Extent{Start: at(25, 0, 0), End: at(25, 0, 0)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := StableExtents(timeseries.ExtentList{test.extent}, test.step, test.phase,
				test.window, test.now)
			if len(got) != 1 || !got[0].End.Equal(test.expectedEnd) {
				t.Fatalf("expected a stable end of %s, got %v", test.expectedEnd, got)
			}
			for _, e := range got {
				if !timeseries.OnGrid(e.Start, test.step, test.phase) ||
					!timeseries.OnGrid(e.End, test.step, test.phase) {
					t.Errorf("stable extent %v is off the grid", e)
				}
			}
			gaps := got.CalculateDeltas(timeseries.ExtentList{test.extent}, test.step)
			if len(gaps) != 1 || !gaps[0].Start.Equal(test.expectedGap.Start) ||
				!gaps[0].End.Equal(test.expectedGap.End) {
				t.Errorf("expected gap %s, got %v", test.expectedGap, gaps)
			}
		})
	}
}

func TestExecuteDeltaNarrowsOffGridCoverage(t *testing.T) {
	// an entry stored with coverage ending between buckets must refetch the bucket that the
	// coverage cut through, starting at that bucket's boundary
	at := func(d, h, m int) time.Time { return time.Date(2024, 1, d, h, m, 0, 0, time.UTC) }
	const backend = "offgrid-engine-test"
	cache := newTestCache()
	engine := New(Config{
		Protocol: "test", BackendName: backend,
		CacheClient: func() trickstercache.Cache { return cache },
		CacheTTL:    time.Minute,
	}, testCodec{})
	engine.Store("dpc", &Entry[*payload]{
		Payload: &payload{Statements: []string{"cached"}},
		Extents: timeseries.ExtentList{{Start: at(1, 22, 30), End: at(1, 23, 0)}},
	})
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "canonical", Step: time.Hour, Phase: 30 * time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: at(1, 22, 30), Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: at(2, 1, 30)},
		Renderer:   testRenderer{},
	}
	counter := metrics.TimeseriesOffGridExtents.WithLabelValues(backend, "test")
	before := testutil.ToFloat64(counter)
	var counts int
	response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, testOps(&counts)))
	if err != nil {
		t.Fatal(err)
	}
	if cacheStatus != status.LookupStatusPartialHit || counts != 1 {
		t.Fatalf("expected one partial-hit fetch, got %s after %d fetches", cacheStatus, counts)
	}
	want := fmt.Sprintf("range(%d,%d)", at(1, 23, 30).Unix(), at(2, 0, 30).Unix())
	if len(response.Statements) != 2 || response.Statements[1] != want {
		t.Errorf("expected the refetch %s, got %v", want, response.Statements)
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("expected 1 off-grid count, got %v", got)
	}
}
