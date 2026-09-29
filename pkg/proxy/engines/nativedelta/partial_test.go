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
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	partialStart = "partial(0,30,60)"
	partialEnd   = "partial(600,600,630)"
	interior     = "range(60,540)"
	truncated    = "range(0,540)"
)

func rawPlan(lower, upper int64) *sqlanalyzer.QueryPlan {
	// a plan whose raw bounds are the client's; a negative upper leaves it open
	plan := testPlan(lower, upper)
	plan.RawLower = &sqlanalyzer.Bound{Value: time.Unix(lower, 0), Inclusive: true}
	if upper < 0 {
		plan.UpperBound = nil
		return plan
	}
	plan.RawUpper = &sqlanalyzer.Bound{Value: time.Unix(upper, 0)}
	return plan
}

type partialRecorder struct {
	mtx     sync.Mutex
	fetched []string
	ttls    []time.Duration
}

func (r *partialRecorder) statements() []string {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return slices.Clone(r.fetched)
}

func partialOps(engine *Engine[*payload], counts *int) (DeltaOps[*payload], *partialRecorder) {
	// partial buckets go through the engine's own object tier, recording each origin fetch
	ops := testOps(counts)
	r := &partialRecorder{}
	ops.FetchPartial = func(ctx context.Context, statement string, ttl time.Duration) (*payload, status.LookupStatus, error) {
		return engine.ExecuteObject("partial:"+statement, ttl, func() (*payload, error) {
			r.mtx.Lock()
			r.fetched, r.ttls = append(r.fetched, statement), append(r.ttls, ttl)
			r.mtx.Unlock()
			return &payload{Statements: []string{statement}}, nil
		})
	}
	ops.Model = func(p *payload) (*Delta, error) { return testRows(p.Statements[0]), nil }
	return ops, r
}

func modeRequest(plan *sqlanalyzer.QueryPlan, ops DeltaOps[*payload], mode timeseries.StepAlignment,
) DeltaRequest[*payload] {
	req := deltaRequest(plan, ops)
	req.StepAlignment = mode
	return req
}

func points(o Outcome[*payload]) map[int64]string {
	// the statement that answered each epoch, in seconds
	out := make(map[int64]string)
	for _, r := range o.Delta.DS.Results {
		for row := range r.Rows(dataset.RowOrder{}) {
			out[int64(row.Point.Epoch)/int64(time.Second)] = row.Point.Values[0].(string)
		}
	}
	return out
}

func partialFetchCount(edge, result string) float64 {
	return testutil.ToFloat64(metrics.ProxyPartialBucketFetches.WithLabelValues("test-backend", "test", edge, result))
}

func TestPartialBucketsByMode(t *testing.T) {
	// [30, 630) at a 1m step: an unaligned start in bucket 0 and an unaligned end in bucket 600
	for _, test := range []struct {
		mode          timeseries.StepAlignment
		interior      string
		start, end    bool
		wantResponse  []string
		wantOriginals []string
	}{
		{mode: 0, interior: interior, wantResponse: []string{interior}},
		{mode: timeseries.StepAlignmentDrop, interior: interior, wantResponse: []string{interior}},
		{mode: timeseries.StepAlignmentTruncate, interior: truncated, wantResponse: []string{truncated}},
		{
			mode: timeseries.StepAlignmentPartial, interior: interior, start: true, end: true,
			wantResponse: []string{partialStart, interior, partialEnd},
		},
		{
			mode: timeseries.StepAlignmentPartialStart, interior: interior, start: true,
			wantResponse: []string{partialStart, interior},
		},
		{
			mode: timeseries.StepAlignmentPartialEnd, interior: truncated, end: true,
			wantResponse: []string{truncated, partialEnd},
		},
	} {
		t.Run(test.mode.String(), func(t *testing.T) {
			engine := newTestEngine(newTestCache())
			counts := 0
			ops, recorder := partialOps(engine, &counts)
			var wantPartials []string
			if test.start {
				wantPartials = append(wantPartials, partialStart)
			}
			if test.end {
				wantPartials = append(wantPartials, partialEnd)
			}
			startMiss, endMiss := partialFetchCount("start", "kmiss"), partialFetchCount("end", "kmiss")
			startHit, endHit := partialFetchCount("start", "hit"), partialFetchCount("end", "hit")
			for i, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
				response, cacheStatus, err := engine.ExecuteDelta(modeRequest(rawPlan(30, 630), ops, test.mode))
				if err != nil || cacheStatus != want || !slices.Equal(statements(response), test.wantResponse) {
					t.Fatalf("request %d = %v, %s, %v; want %v, %s", i, statements(response), cacheStatus, err,
						test.wantResponse, want)
				}
				// each partial bucket answers its label alone, beside every interior label
				got, first := points(response), int64(60)
				if test.interior == truncated {
					first = 0
				}
				wantPoints := map[int64]string{}
				for at := first; at <= 540; at += 60 {
					wantPoints[at] = test.interior
				}
				if test.start {
					wantPoints[0] = partialStart
				}
				if test.end {
					wantPoints[600] = partialEnd
				}
				if !maps.Equal(got, wantPoints) {
					t.Fatalf("request %d answered %v, want %v", i, got, wantPoints)
				}
			}
			// the interior was fetched once, and each partial bucket once, the repeat from the object tier
			if counts != 1 || !slices.Equal(recorder.statements(), wantPartials) {
				t.Fatalf("fetched the interior %d times and partials %v, want %v", counts, recorder.statements(),
					wantPartials)
			}
			for _, ttl := range recorder.ttls {
				if ttl != 15*time.Second {
					t.Errorf("a partial bucket was kept for %s", ttl)
				}
			}
			// the delta tier holds the complete buckets only
			cached, found := engine.deltas.retrieve("dpc")
			if !found || !slices.Equal(statements(Outcome[*payload]{Delta: cached.Payload}), []string{test.interior}) {
				t.Fatalf("delta entry holds %v", statements(Outcome[*payload]{Delta: cached.Payload}))
			}
			for _, check := range []struct {
				edge      string
				fetched   bool
				miss, hit float64
			}{{"start", test.start, startMiss, startHit}, {"end", test.end, endMiss, endHit}} {
				want := 0.0
				if check.fetched {
					want = 1
				}
				if got := partialFetchCount(check.edge, "kmiss") - check.miss; got != want {
					t.Errorf("%s misses counted %v, want %v", check.edge, got, want)
				}
				if got := partialFetchCount(check.edge, "hit") - check.hit; got != want {
					t.Errorf("%s hits counted %v, want %v", check.edge, got, want)
				}
			}
		})
	}
}

func TestLivePartialBucket(t *testing.T) {
	now := time.Unix(3630, 0)
	engine := newTestEngine(newTestCache())
	counts := 0
	ops, recorder := partialOps(engine, &counts)
	for _, test := range []struct {
		upper int64
		want  []string
	}{
		// a closed range ends the live bucket's fetch at the client's end, and an open one leaves it open
		{upper: 3620, want: []string{"range(3000,3540)", "partial(3600,3600,3620)"}},
		{upper: -1, want: []string{"range(3000,3540)", "partial(3600,3600,-1)"}},
	} {
		req := modeRequest(rawPlan(3000, test.upper), ops, timeseries.StepAlignmentPartialEnd)
		req.Now, req.RequireUpperBound = now, false
		response, _, err := engine.ExecuteDelta(req)
		if err != nil || !slices.Equal(statements(response), test.want) {
			t.Fatalf("upper %d = %v, %v; want %v", test.upper, statements(response), err, test.want)
		}
		if got := recorder.statements(); got[len(got)-1] != test.want[1] {
			t.Fatalf("fetched %v", got)
		}
	}
	// the live bucket never reaches the delta tier
	if cached, found := engine.deltas.retrieve("dpc"); !found ||
		!slices.Equal(statements(Outcome[*payload]{Delta: cached.Payload}), []string{"range(3000,3540)"}) {
		t.Fatalf("delta entry = %+v, %t", cached, found)
	}
}

func TestPartialBucketFailuresLeaveTheBucketOut(t *testing.T) {
	originDown := errors.New("origin down")
	for name, test := range map[string]struct {
		change  func(ops *DeltaOps[*payload], plan *sqlanalyzer.QueryPlan)
		rewrite bool
	}{
		"fetch": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.FetchPartial = func(context.Context, string, time.Duration) (*payload, status.LookupStatus, error) {
				return nil, status.LookupStatusProxyError, originDown
			}
		}},
		"model": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.Model = func(*payload) (*Delta, error) { return nil, Unmergeable(originDown) }
		}},
		"rows": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.Model = func(*payload) (*Delta, error) { return &Delta{}, nil }
		}},
		"columns": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.Model = func(p *payload) (*Delta, error) {
				rows := testRows(p.Statements[0])
				rows.Header = []byte("other columns")
				return rows, nil
			}
		}},
		"no object tier": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.FetchPartial = nil
		}},
		"no model": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.Model = nil
		}},
		"render": {change: func(_ *DeltaOps[*payload], plan *sqlanalyzer.QueryPlan) {
			plan.Renderer = testRenderer{rangeErr: sqlanalyzer.ErrUnsupportedRange}
		}, rewrite: true},
		"panic": {change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
			ops.ConcurrentPartials = true
			ops.Model = func(*payload) (*Delta, error) { panic("model") }
		}},
	} {
		t.Run(name, func(t *testing.T) {
			var rewrites int
			engine := New(Config{
				Protocol: "test", BackendName: "test-backend", CacheTTL: time.Minute,
				CacheClient:           func() trickstercache.Cache { return newTestCache() },
				ObserveRewriteFailure: func(string) { rewrites++ },
			}, testCodec{})
			counts := 0
			ops, _ := partialOps(engine, &counts)
			plan := rawPlan(30, 630)
			test.change(&ops, plan)
			failed := partialFetchCount("start", partialStatusErr)
			response, cacheStatus, err := engine.ExecuteDelta(modeRequest(plan, ops, timeseries.StepAlignmentPartial))
			// the interior answers as it would alone, with its own status
			if err != nil || cacheStatus != status.LookupStatusKeyMiss ||
				!slices.Equal(statements(response), []string{interior}) {
				t.Fatalf("= %v, %s, %v", statements(response), cacheStatus, err)
			}
			if got := partialFetchCount("start", partialStatusErr) - failed; got != 1 {
				t.Errorf("failures counted %v", got)
			}
			if test.rewrite != (rewrites == 2) {
				t.Errorf("rewrite failures counted %d", rewrites)
			}
		})
	}
}

func TestConcurrentPartialsRunBesideTheInterior(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	ops, recorder := partialOps(engine, &counts)
	ops.ConcurrentPartials = true
	started := make(chan struct{})
	fetchPartial := ops.FetchPartial
	var once sync.Once
	ops.FetchPartial = func(ctx context.Context, statement string, ttl time.Duration) (*payload, status.LookupStatus, error) {
		once.Do(func() { close(started) })
		return fetchPartial(ctx, statement, ttl)
	}
	fetch := ops.Fetch
	ops.Fetch = func(statement string) (*Delta, error) {
		// the interior's fetch finishes only once a partial bucket's has begun
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			return nil, errors.New("the partial buckets waited for the interior")
		}
		return fetch(statement)
	}
	response, _, err := engine.ExecuteDelta(modeRequest(rawPlan(30, 630), ops, timeseries.StepAlignmentPartial))
	if err != nil || !slices.Equal(statements(response), []string{partialStart, interior, partialEnd}) {
		t.Fatalf("= %v, %v", statements(response), err)
	}

	// an interior answered by the object path discards them
	ops.Fetch = func(string) (*Delta, error) { return nil, Unmergeable(errors.New("unmodeled")) }
	response, _, err = engine.ExecuteDelta(modeRequest(rawPlan(3030, 3630), ops, timeseries.StepAlignmentPartial))
	if err != nil || !slices.Equal(statements(response), []string{"object"}) {
		t.Fatalf("object path = %v, %v", statements(response), err)
	}
	// a plan already marked unmergeable doesn't fetch them
	fetched, partials := counts, len(recorder.statements())
	response, _, err = engine.ExecuteDelta(modeRequest(rawPlan(3031, 3631), ops, timeseries.StepAlignmentPartial))
	if err != nil || !slices.Equal(statements(response), []string{"object"}) || counts != fetched ||
		len(recorder.statements()) != partials {
		t.Fatalf("marked plan = %v, %v", statements(response), err)
	}
}

func TestPartialBucketsAreFetchedWithoutTheKeysLock(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	ops, _ := partialOps(engine, &counts)
	fetchPartial := ops.FetchPartial
	ops.FetchPartial = func(ctx context.Context, statement string, ttl time.Duration) (*payload, status.LookupStatus, error) {
		engine.lockMtx.Lock()
		_, held := engine.locks["dpc"]
		engine.lockMtx.Unlock()
		if held {
			t.Error("a partial bucket was fetched under the key's lock")
		}
		return fetchPartial(ctx, statement, ttl)
	}
	if _, _, err := engine.ExecuteDelta(modeRequest(rawPlan(30, 630), ops, timeseries.StepAlignmentPartial)); err != nil {
		t.Fatal(err)
	}
}

func TestStepAlignmentResolution(t *testing.T) {
	engine := newTestEngine(nil)
	fallbacks := func(requested timeseries.StepAlignment) float64 {
		return testutil.ToFloat64(metrics.StepAlignmentFallbacks.WithLabelValues("test-backend",
			requested.String(), timeseries.StepAlignmentNameDrop))
	}
	for requested, want := range map[timeseries.StepAlignment]timeseries.StepAlignment{
		0:                                    timeseries.StepAlignmentDrop,
		timeseries.StepAlignmentPartial:      timeseries.StepAlignmentPartial,
		timeseries.StepAlignmentTruncate:     timeseries.StepAlignmentTruncate,
		timeseries.StepAlignmentOff:          timeseries.StepAlignmentDrop,
		timeseries.StepAlignmentAll:          timeseries.StepAlignmentDrop,
		timeseries.StepAlignmentPartialStart: timeseries.StepAlignmentPartialStart,
	} {
		before := fallbacks(requested)
		if got := engine.stepAlignment(requested); got != want {
			t.Errorf("%s resolved to %s", requested, got)
		}
		if counted := fallbacks(requested) - before; (counted == 1) != (requested != 0 && want != requested) {
			t.Errorf("%s counted %v fallbacks", requested, counted)
		}
	}
}

func TestAbandonedPartialsNeverDelayAFallback(t *testing.T) {
	// an interior that isn't answered from the delta tier cancels its concurrent partial buckets and
	// returns without them, however long they would take
	for name, test := range map[string]struct {
		change func(ops *DeltaOps[*payload], plan *sqlanalyzer.QueryPlan)
		check  func(o Outcome[*payload], lookup status.LookupStatus, err error) bool
	}{
		"object path": {
			change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
				ops.Fetch = func(string) (*Delta, error) { return nil, Unmergeable(errors.New("unmodeled")) }
			},
			check: func(o Outcome[*payload], _ status.LookupStatus, err error) bool {
				return err == nil && slices.Equal(statements(o), []string{"object"})
			},
		},
		"proxy error": {
			change: func(ops *DeltaOps[*payload], _ *sqlanalyzer.QueryPlan) {
				ops.Fetch = func(string) (*Delta, error) { return nil, errors.New("origin down") }
			},
			check: func(o Outcome[*payload], lookup status.LookupStatus, err error) bool {
				return err != nil && lookup == status.LookupStatusProxyError && o.Delta == nil
			},
		},
		"original": {
			change: func(_ *DeltaOps[*payload], plan *sqlanalyzer.QueryPlan) {
				plan.Renderer = testRenderer{err: errors.New("render failed")}
			},
			check: func(o Outcome[*payload], lookup status.LookupStatus, err error) bool {
				return err == nil && lookup == status.LookupStatusProxyOnly &&
					slices.Equal(statements(o), []string{"original"})
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newTestEngine(newTestCache())
			counts := 0
			ops, _ := partialOps(engine, &counts)
			ops.ConcurrentPartials = true
			cancelled := make(chan struct{}, 2)
			ops.FetchPartial = func(ctx context.Context, _ string, _ time.Duration) (*payload, status.LookupStatus, error) {
				// blocks until the engine gives up on it
				select {
				case <-ctx.Done():
					cancelled <- struct{}{}
					return nil, status.LookupStatusProxyError, ctx.Err()
				case <-time.After(time.Minute):
					return &payload{Statements: []string{"late"}}, status.LookupStatusKeyMiss, nil
				}
			}
			plan := rawPlan(30, 630)
			test.change(&ops, plan)
			done := make(chan struct{})
			var (
				o      Outcome[*payload]
				lookup status.LookupStatus
				err    error
			)
			go func() {
				defer close(done)
				o, lookup, err = engine.ExecuteDelta(modeRequest(plan, ops, timeseries.StepAlignmentPartial))
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the fallback waited for its partial buckets")
			}
			if !test.check(o, lookup, err) {
				t.Fatalf("= %v, %s, %v", statements(o), lookup, err)
			}
			for range 2 {
				select {
				case <-cancelled:
				case <-time.After(5 * time.Second):
					t.Fatal("a discarded partial bucket fetch was never cancelled")
				}
			}
		})
	}
}

func TestPartialBucketsFollowTheRequestsContext(t *testing.T) {
	engine := newTestEngine(newTestCache())
	counts := 0
	ops, _ := partialOps(engine, &counts)
	type key struct{}
	var seen []any
	fetchPartial := ops.FetchPartial
	ops.FetchPartial = func(ctx context.Context, statement string, ttl time.Duration) (*payload, status.LookupStatus, error) {
		seen = append(seen, ctx.Value(key{}))
		return fetchPartial(ctx, statement, ttl)
	}
	req := modeRequest(rawPlan(30, 630), ops, timeseries.StepAlignmentPartial)
	req.Context = context.WithValue(context.Background(), key{}, "request")
	if _, _, err := engine.ExecuteDelta(req); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "request" || seen[1] != "request" {
		t.Fatalf("partial fetches saw %v", seen)
	}
}
