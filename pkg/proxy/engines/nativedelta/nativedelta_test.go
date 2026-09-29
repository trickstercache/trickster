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
	"slices"
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
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

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
	ttls        map[string]time.Duration
	storeErr    error
	retrieveErr error
	removeErr   error
	provider    string
	shared      bool // Retrieve returns the stored bytes themselves, as bbolt and memory do
}

func newTestCache() *testCache {
	return &testCache{data: make(map[string][]byte), ttls: make(map[string]time.Duration)}
}

func (c *testCache) Connect() error { return nil }
func (c *testCache) Close() error   { return nil }

func (c *testCache) Store(key string, data []byte, ttl time.Duration) error {
	if c.storeErr != nil {
		return c.storeErr
	}
	c.mtx.Lock()
	c.data[key] = append([]byte(nil), data...)
	c.ttls[key] = ttl
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
	if c.shared {
		return data, status.LookupStatusHit, nil
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

func (c *testCache) Configuration() *cacheoptions.Options {
	o := cacheoptions.New()
	if c.provider != "" {
		o.Provider = c.provider
	}
	return o
}

func newTestEngine(c trickstercache.Cache) *Engine[*payload] {
	return New(Config{
		Protocol: "test", BackendName: "test-backend",
		CacheClient: func() trickstercache.Cache { return c },
		CacheTTL:    time.Minute,
	}, testCodec{})
}

// testRenderer renders extents as "range(start,end)" and partial buckets as "partial(label,lower,upper)",
// with an open upper as -1.
type testRenderer struct{ err, rangeErr error }

func (r testRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return fmt.Sprintf("range(%d,%d)", extent.Start.Unix(), extent.End.Unix()), nil
}

func (r testRenderer) RenderRange(pb timeseries.PartialBucket) (string, error) {
	if r.rangeErr != nil {
		return "", r.rangeErr
	}
	upper := int64(-1)
	if !pb.Upper.IsZero() {
		upper = pb.Upper.Unix()
	}
	return fmt.Sprintf("partial(%d,%d,%d)", pb.Label.Unix(), pb.Lower.Unix(), upper), nil
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

const testHeader = "columns"

func testOps(counts *int) DeltaOps[*payload] {
	// rows carry the statement that fetched them, so tests can assert which fetches composed a response
	return DeltaOps[*payload]{
		Fetch: func(statement string) (*Delta, error) {
			*counts++
			return testRows(statement), nil
		},
		FetchOriginal: func() (*payload, error) {
			*counts++
			return &payload{Statements: []string{"original"}}, nil
		},
		ObjectFallback: func() (*payload, status.LookupStatus, error) {
			return &payload{Statements: []string{"object"}}, status.LookupStatusKeyMiss, nil
		},
	}
}

func testRows(statement string) *Delta {
	// range(start,end) gives a point a minute, a partial bucket its label and both neighbors (a crop to
	// the label drops them), anything else one point; each holds the statement
	s := &dataset.Series{Header: dataset.SeriesHeader{Name: "rows"}}
	ds := &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{s}}}}
	var start, end, label, lower, upper int64
	if _, err := fmt.Sscanf(statement, "partial(%d,%d,%d)", &label, &lower, &upper); err == nil {
		start, end = label-60, label+60
	} else if _, err := fmt.Sscanf(statement, "range(%d,%d)", &start, &end); err != nil {
		start, end = 0, 0
	}
	for at := start; at <= end; at += 60 {
		s.Points = append(s.Points, dataset.Point{
			Epoch: epoch.Epoch(time.Duration(at) * time.Second), Size: 1, Values: []any{statement},
		})
	}
	ds.ExtentList = timeseries.ExtentList{{Start: time.Unix(start, 0), End: time.Unix(end, 0)}}
	return &Delta{Header: []byte(testHeader), DS: ds}
}

func statements(o Outcome[*payload]) []string {
	// the statements whose rows compose a response, in time order, or the object's own
	if o.Delta == nil {
		if o.Object == nil {
			return nil
		}
		return o.Object.Statements
	}
	var out []string
	for _, r := range o.Delta.DS.Results {
		for row := range r.Rows(dataset.RowOrder{}) {
			if st := row.Point.Values[0].(string); len(out) == 0 || out[len(out)-1] != st {
				out = append(out, st)
			}
		}
	}
	return out
}

var testNow = time.Unix(2_000_000_000, 0) // every test window has ended by then

func deltaRequest(plan *sqlanalyzer.QueryPlan, ops DeltaOps[*payload]) DeltaRequest[*payload] {
	return DeltaRequest[*payload]{
		Key: "dpc", FallbackKey: "dpc-fallback", Statement: "client",
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
		!slices.Equal(statements(response), []string{"range(0,540)"}) {
		t.Fatalf("full miss = %v, %s, %v (fetches=%d)", statements(response), cacheStatus, err, counts)
	}

	// identical window is a pure hit with no upstream fetch
	response, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
	if err != nil || cacheStatus != status.LookupStatusHit || counts != 1 ||
		!slices.Equal(statements(response), []string{"range(0,540)"}) {
		t.Fatalf("hit = %v, %s, %v (fetches=%d)", statements(response), cacheStatus, err, counts)
	}

	// widened window fetches only the missing extent and merges
	response, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 900), ops))
	if err != nil || cacheStatus != status.LookupStatusPartialHit || counts != 2 {
		t.Fatalf("partial = %v, %s, %v (fetches=%d)", statements(response), cacheStatus, err, counts)
	}
	if got := statements(response); !slices.Equal(got, []string{"range(0,540)", "range(600,840)"}) {
		t.Fatalf("partial fetch composed %v", got)
	}

	// a disjoint window is a range miss
	_, cacheStatus, err = engine.ExecuteDelta(deltaRequest(testPlan(7200, 7800), ops))
	if err != nil || cacheStatus != status.LookupStatusRangeMiss {
		t.Fatalf("range miss status = %s, %v", cacheStatus, err)
	}

	// a delta entry stored directly is the one the next request finds
	seeded := testRows("range(0,540)")
	engine.StoreDelta("dpc", &Entry[*Delta]{Payload: seeded, Extents: seeded.DS.ExtentList})
	if got, found := engine.RetrieveDelta("dpc"); !found || got.Payload.Rows() != 10 {
		t.Fatalf("seeded entry = %+v, %t", got, found)
	}
	if _, cacheStatus, _ = engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops)); cacheStatus != status.LookupStatusHit {
		t.Fatalf("seeded entry status = %s", cacheStatus)
	}
}

func TestExecuteDeltaFallbacks(t *testing.T) {
	t.Run("unsupported bounds proxy the original", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		response, cacheStatus, err := engine.ExecuteDelta(
			deltaRequest(&sqlanalyzer.QueryPlan{}, testOps(&counts)))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			!slices.Equal(statements(response), []string{"original"}) {
			t.Fatalf("bounds fallback = %v, %s, %v", statements(response), cacheStatus, err)
		}
	})

	t.Run("render failure proxies the original", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		plan := testPlan(0, 600)
		plan.Renderer = testRenderer{err: errors.New("render failed")}
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, testOps(&counts)))
		if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
			!slices.Equal(statements(response), []string{"original"}) {
			t.Fatalf("render fallback = %v, %s, %v", statements(response), cacheStatus, err)
		}
	})

	t.Run("fetch failure is a proxy error", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		ops.Fetch = func(string) (*Delta, error) { return nil, errors.New("origin down") }
		_, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err == nil || cacheStatus != status.LookupStatusProxyError {
			t.Fatalf("fetch failure = %s, %v", cacheStatus, err)
		}
	})

	t.Run("unmergeable marks the plan and serves the object path", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		ops.Fetch = func(string) (*Delta, error) {
			counts++
			return nil, Unmergeable(errors.New("schema not representable"))
		}
		response, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || !slices.Equal(statements(response), []string{"object"}) {
			t.Fatalf("unmergeable fallback = %v, %v", statements(response), err)
		}
		if _, blocked := engine.Retrieve("dpc-fallback"); !blocked {
			t.Fatal("fallback marker was not stored")
		}
		// later requests skip the delta fetch entirely
		fetchesBefore := counts
		response, _, err = engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
		if err != nil || !slices.Equal(statements(response), []string{"object"}) || counts != fetchesBefore {
			t.Fatalf("marker did not short-circuit: %v (fetches %d->%d)",
				statements(response), fetchesBefore, counts)
		}
	})

	t.Run("columns that change between fetches are unmergeable", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		ops := testOps(&counts)
		if _, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops)); err != nil {
			t.Fatal(err)
		}
		ops.Fetch = func(statement string) (*Delta, error) {
			rows := testRows(statement)
			rows.Header = []byte("other columns")
			return rows, nil
		}
		response, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 900), ops))
		if err != nil || !slices.Equal(statements(response), []string{"object"}) {
			t.Fatalf("changed columns = %v, %v", statements(response), err)
		}
		if _, found := engine.deltas.retrieve("dpc"); found {
			t.Fatal("the delta entry holding the old columns was kept")
		}
		// a listener can accept headers that differ only in what doesn't shape rows
		engine = newTestEngine(newTestCache())
		ops = testOps(&counts)
		if _, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops)); err != nil {
			t.Fatal(err)
		}
		ops.Fetch = func(statement string) (*Delta, error) {
			rows := testRows(statement)
			rows.Header = []byte("other columns")
			return rows, nil
		}
		ops.SameHeader = func(a, b []byte) bool { return true }
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 900), ops))
		if err != nil || cacheStatus != status.LookupStatusPartialHit || string(response.Delta.Header) != "other columns" {
			t.Fatalf("accepted columns = %v, %s, %v", statements(response), cacheStatus, err)
		}
	})

	t.Run("an entry with no rows refetches from scratch", func(t *testing.T) {
		engine := newTestEngine(newTestCache())
		counts := 0
		engine.deltas.store("dpc", &Entry[*Delta]{
			Payload: &Delta{Header: []byte(testHeader)},
			Extents: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(540, 0)}},
		}, time.Minute)
		response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), testOps(&counts)))
		if err != nil || cacheStatus != status.LookupStatusKeyMiss || counts != 1 ||
			!slices.Equal(statements(response), []string{"range(0,540)"}) {
			t.Fatalf("invalid entry retry = %v, %s, %v (fetches=%d)", statements(response), cacheStatus, err, counts)
		}
	})
}

func TestRangeWithoutACompleteBucketIsTheOriginsAnswer(t *testing.T) {
	counts := 0
	plan := testPlan(0, 30)
	engine := newTestEngine(newTestCache())
	response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(plan, testOps(&counts)))
	if err != nil || cacheStatus != status.LookupStatusProxyOnly ||
		!slices.Equal(statements(response), []string{"original"}) {
		t.Fatalf("without an object tier = %v, %s, %v", statements(response), cacheStatus, err)
	}
	c := newTestCache()
	engine = New(Config{
		Protocol: "test", BackendName: "test-backend", CacheTTL: time.Minute, PartialBucketTTL: 7 * time.Second,
		CacheClient: func() trickstercache.Cache { return c },
	}, testCodec{})
	ops, fetched := partialOps(engine, &counts)
	for _, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
		response, cacheStatus, err = engine.ExecuteDelta(deltaRequest(plan, ops))
		if err != nil || cacheStatus != want || !slices.Equal(statements(response), []string{"client"}) {
			t.Fatalf("= %v, %s, %v; want %s", statements(response), cacheStatus, err, want)
		}
	}
	// the client's own statement, whole, for the partial bucket TTL; the delta tier is never touched
	if !slices.Equal(fetched.statements(), []string{"client"}) || c.ttls["partial:client"] != 7*time.Second {
		t.Fatalf("fetched %v, stored for %s", fetched.statements(), c.ttls["partial:client"])
	}
	if _, found := engine.deltas.retrieve("dpc"); found {
		t.Fatal("a range without a complete bucket was delta-cached")
	}
}

func TestExecuteObject(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	fetch := func() (*payload, error) {
		counts++
		return &payload{Statements: []string{"whole"}}, nil
	}
	response, cacheStatus, err := engine.ExecuteObject("opc", 0, fetch)
	if err != nil || cacheStatus != status.LookupStatusKeyMiss || counts != 1 ||
		response.Statements[0] != "whole" {
		t.Fatalf("object miss = %+v, %s, %v", response, cacheStatus, err)
	}
	_, cacheStatus, err = engine.ExecuteObject("opc", 0, fetch)
	if err != nil || cacheStatus != status.LookupStatusHit || counts != 1 {
		t.Fatalf("object hit = %s, %v (fetches=%d)", cacheStatus, err, counts)
	}
	_, cacheStatus, err = engine.ExecuteObject("opc-err", 0, func() (*payload, error) {
		return nil, errors.New("origin down")
	})
	if err == nil || cacheStatus != status.LookupStatusProxyError {
		t.Fatalf("object error = %s, %v", cacheStatus, err)
	}
}

func TestExecuteObjectTTL(t *testing.T) {
	c := newTestCache()
	engine := newTestEngine(c)
	fetch := func() (*payload, error) { return &payload{Statements: []string{"whole"}}, nil }
	for _, test := range []struct {
		key      string
		ttl, got time.Duration
	}{
		{"configured", 0, time.Minute},
		{"given", 5 * time.Second, 5 * time.Second},
	} {
		if _, _, err := engine.ExecuteObject(test.key, test.ttl, fetch); err != nil {
			t.Fatal(err)
		}
		if c.ttls[test.key] != test.got {
			t.Errorf("%s: stored for %s, want %s", test.key, c.ttls[test.key], test.got)
		}
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
		window, err := BuildWindow(testPlan(5, 185), now, true, timeseries.StepAlignmentDrop)
		if err != nil {
			t.Fatal(err)
		}
		if !window.Output.Start.Equal(time.Unix(60, 0)) ||
			!window.Output.End.Equal(time.Unix(120, 0)) || window.Empty {
			t.Fatalf("window = %+v", window)
		}
	})

	t.Run("sub-cadence range is empty", func(t *testing.T) {
		window, err := BuildWindow(testPlan(5, 25), now, true, timeseries.StepAlignmentDrop)
		if err != nil || !window.Empty || !window.Output.Start.Equal(time.Unix(60, 0)) {
			t.Fatalf("window = %+v, %v", window, err)
		}
	})

	t.Run("open upper runs to the still-filling bucket when allowed", func(t *testing.T) {
		plan := testPlan(0, 0)
		plan.UpperBound = nil
		if _, err := BuildWindow(plan, now, true, timeseries.StepAlignmentDrop); !errors.Is(err, ErrUnsupportedBounds) {
			t.Fatalf("required upper bound accepted an open plan: %v", err)
		}
		for _, now := range []time.Time{now, now.Add(30 * time.Second)} {
			window, err := BuildWindow(plan, now, false, timeseries.StepAlignmentDrop)
			if err != nil || !window.Output.End.Equal(time.Unix(3540, 0)) || window.PartialCount != 0 {
				t.Fatalf("open window at %d = %+v, %v", now.Unix(), window, err)
			}
		}
	})

	t.Run("buckets that have not ended are left out", func(t *testing.T) {
		window, err := BuildWindow(testPlan(3000, 7200), now.Add(30*time.Second), true, timeseries.StepAlignmentDrop)
		if err != nil || !window.Output.End.Equal(time.Unix(3540, 0)) {
			t.Fatalf("window = %+v, %v", window, err)
		}
		if window, err = BuildWindow(testPlan(3600, 7200), now.Add(30*time.Second), true,
			timeseries.StepAlignmentDrop); err != nil ||
			!window.Empty {
			t.Fatalf("a window of unfinished buckets = %+v, %v", window, err)
		}
	})

	t.Run("inclusive upper names the final bucket", func(t *testing.T) {
		plan := testPlan(0, 0)
		plan.UpperBound = &sqlanalyzer.Bound{Value: time.Unix(600, 0), Inclusive: true}
		if _, err := BuildWindow(plan, now, true, timeseries.StepAlignmentDrop); !errors.Is(err, ErrUnsupportedBounds) {
			t.Fatalf("required exclusive upper accepted inclusive: %v", err)
		}
		window, err := BuildWindow(plan, now, false, timeseries.StepAlignmentDrop)
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
			if _, err := BuildWindow(plan, now, false, timeseries.StepAlignmentDrop); err == nil {
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
		// a query's window replaces the configured one, narrower or wider
		{5 * time.Minute, time.Minute, 30 * time.Second, 30 * time.Second, 10},
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
	engine.deltas.store("dpc", &Entry[*Delta]{
		Payload: testRows("cached"),
		Extents: timeseries.ExtentList{{Start: at(1, 22, 30), End: at(1, 23, 0)}},
	}, time.Minute)
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
	if got := statements(response); len(got) != 1 || got[0] != want {
		t.Errorf("expected the refetch %s, got %v", want, got)
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("expected 1 off-grid count, got %v", got)
	}
}

func TestExecuteDeltaStoresOnlyRetainedStableRows(t *testing.T) {
	// retention and the volatile window shrink what is stored, never the response, and the next
	// request refetches exactly what was left out
	for _, test := range []struct {
		name    string
		cfg     func(*Config)
		refetch string
	}{
		{"retention", func(c *Config) { c.RetentionPoints = 5 }, "range(0,240)"},
		{"volatile window", func(c *Config) { c.VolatileWindow = time.Since(time.Unix(300, 0)) }, "range(300,540)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Protocol: "test", BackendName: "retain-test", CacheTTL: time.Minute,
				CacheClient: func() trickstercache.Cache { return nil },
			}
			cacheClient := newTestCache()
			cfg.CacheClient = func() trickstercache.Cache { return cacheClient }
			test.cfg(&cfg)
			engine := New(cfg, testCodec{})
			counts := 0
			ops := testOps(&counts)
			response, _, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
			if err != nil || response.Delta.Rows() != 10 {
				t.Fatalf("first response = %d rows, %v", response.Delta.Rows(), err)
			}
			var fetched []string
			ops.Fetch = func(statement string) (*Delta, error) {
				fetched = append(fetched, statement)
				return testRows(statement), nil
			}
			response, cacheStatus, err := engine.ExecuteDelta(deltaRequest(testPlan(0, 600), ops))
			if err != nil || cacheStatus != status.LookupStatusPartialHit || response.Delta.Rows() != 10 ||
				!slices.Equal(fetched, []string{test.refetch}) {
				t.Fatalf("repeat = %d rows, %s, %v, fetched %v", response.Delta.Rows(), cacheStatus, err, fetched)
			}
		})
	}
}

func TestDeltaCodec(t *testing.T) {
	rows := testRows("range(0,120)")
	data, err := deltaCodec{}.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deltaCodec{}.Unmarshal(data)
	if err != nil || string(got.Header) != testHeader || got.Rows() != 3 {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if (deltaCodec{}).Size(rows) <= len(testHeader) || (deltaCodec{}).Size(nil) != 0 || (*Delta)(nil).Rows() != 0 {
		t.Error("sizes and row counts")
	}
	for name, corrupt := range map[string][]byte{
		"short": data[:3], "magic": append([]byte("XXXX"), data[4:]...),
		"version": append(append([]byte(nil), data[:4]...), append([]byte{9}, data[5:]...)...),
		"header":  append(append([]byte(nil), data[:5]...), 0xff, 0xff, 0xff, 0xff),
		"rows":    append(append([]byte(nil), data[:9+len(testHeader)]...), 0xc1),
	} {
		if _, err := (deltaCodec{}).Unmarshal(corrupt); err == nil {
			t.Errorf("%s: corrupt rows decoded", name)
		}
	}
	if _, err := (deltaCodec{}).Marshal(&Delta{}); err == nil {
		t.Error("a delta with no rows encoded")
	}
}
