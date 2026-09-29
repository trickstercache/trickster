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
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

const partialBucketTTL = 20 * time.Second

func bucketedUpstream(t testing.TB) func(query string) ([]byte, error) {
	t.Helper()
	// aggregates as the origin would: a row per host per bucket the range covers, valued by the seconds
	// covered, so a partial bucket's value shows how much was fetched
	return func(query string) ([]byte, error) {
		match := renderedBounds.FindStringSubmatch(query)
		if match == nil {
			return nil, fmt.Errorf("no bounds in rendered statement %q", query)
		}
		lower, _ := strconv.ParseInt(match[1], 10, 64)
		upper, _ := strconv.ParseInt(match[2], 10, 64)
		schema := arrow.NewSchema([]arrow.Field{
			{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
			{Name: "host", Type: arrow.BinaryTypes.String},
			{Name: "v", Type: arrow.PrimitiveTypes.Float64},
		}, nil)
		builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
		defer builder.Release()
		for bucket := lower - lower%60; bucket < upper; bucket += 60 {
			covered := min(bucket+60, upper) - max(bucket, lower)
			for _, host := range []string{"a", "b"} {
				builder.Field(0).(*array.TimestampBuilder).Append(arrow.Timestamp(bucket * int64(time.Second)))
				builder.Field(1).(*array.StringBuilder).Append(host)
				builder.Field(2).(*array.Float64Builder).Append(float64(covered))
			}
		}
		record := builder.NewRecordBatch()
		defer record.Release()
		return EncodeRecords(schema, []arrow.RecordBatch{record})
	}
}

func partialTestServer(t *testing.T, up *fakeUpstream, mode timeseries.StepAlignment) (*Server, *memCache) {
	t.Helper()
	inner := newMemCache()
	return NewServer(up, inner, WithCacheKeyPrefix("influx3"), WithCacheTTL(time.Hour), WithDeltaCache(DeltaConfig{
		Analyzer:         testAnalyzer,
		CacheClient:      func() trickstercache.Cache { return deltaTestCache{inner: inner} },
		CacheTTL:         time.Hour,
		PartialBucketTTL: partialBucketTTL,
		StepAlignment:    mode,
	})), inner
}

func TestDeltaTierPartialBucketsMatchTheOrigin(t *testing.T) {
	// [30, 630) at a one-minute step: each mode's answer is the origin's over the range it serves
	origin := &fakeUpstream{executeFn: bucketedUpstream(t)}
	direct := NewServer(origin, nil)
	for mode, served := range map[timeseries.StepAlignment][2]int{
		timeseries.StepAlignmentTruncate:     {0, 600},
		timeseries.StepAlignmentDrop:         {60, 600},
		timeseries.StepAlignmentPartial:      {30, 630},
		timeseries.StepAlignmentPartialStart: {30, 600},
		timeseries.StepAlignmentPartialEnd:   {0, 630},
	} {
		t.Run(mode.String(), func(t *testing.T) {
			up := &fakeUpstream{executeFn: bucketedUpstream(t)}
			srv, inner := partialTestServer(t, up, mode)
			want := executeRows(t, direct, fmt.Sprintf(deltaQuery+" ORDER BY time, host", served[0], served[1]))
			query := fmt.Sprintf(deltaQuery+" ORDER BY time, host", 30, 630)
			if got := executeRows(t, srv, query); !reflect.DeepEqual(got, want) {
				t.Fatalf("first answer differs from the origin's:\n%v\n%v", got, want)
			}
			// the interior and one statement per partial bucket, each bounded by the client's own range
			partials := 0
			for _, q := range up.executedQueries {
				if strings.Contains(q, ">= 30)") || strings.Contains(q, "< 630)") {
					partials++
				}
			}
			start, end := mode.Edges()
			wantPartials := 0
			for _, edge := range []timeseries.EdgePolicy{start, end} {
				if edge == timeseries.EdgePartial {
					wantPartials++
				}
			}
			if partials != wantPartials || up.executeCalls != wantPartials+1 {
				t.Fatalf("fetched %q, want %d partial buckets", up.executedQueries, wantPartials)
			}
			// the repeat comes from both tiers
			if got := executeRows(t, srv, query); !reflect.DeepEqual(got, want) || up.executeCalls != wantPartials+1 {
				t.Fatalf("repeat = %v after %d calls", got, up.executeCalls)
			}
			inner.mu.Lock()
			for key, ttl := range inner.ttls {
				if strings.Contains(key, partialStatementKeyKind) != (ttl == partialBucketTTL) {
					t.Errorf("%s stored for %s", key, ttl)
				}
			}
			inner.mu.Unlock()
			// no partial bucket's value reached the delta tier
			whole := fmt.Sprintf(deltaQuery+" ORDER BY time, host", 0, 660)
			if got, full := executeRows(t, srv, whole), executeRows(t, direct, whole); !reflect.DeepEqual(got, full) {
				t.Fatalf("a whole range after partial buckets:\n%v\n%v", got, full)
			}
		})
	}
}

func TestDeltaTierRangeWithoutACompleteBucketIsTheOriginsAnswer(t *testing.T) {
	up := &fakeUpstream{executeFn: bucketedUpstream(t)}
	srv, inner := partialTestServer(t, up, 0)
	query := fmt.Sprintf(deltaQuery, 10, 50)
	want := executeRows(t, NewServer(&fakeUpstream{executeFn: bucketedUpstream(t)}, nil), query)
	for range 2 {
		if got := executeRows(t, srv, query); !reflect.DeepEqual(got, want) || len(want) != 2 {
			t.Fatalf("= %v, want the origin's %v", got, want)
		}
	}
	if up.executeCalls != 1 || up.executedQueries[0] != query {
		t.Fatalf("the origin received %q, want the client's statement once", up.executedQueries)
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.ttls) != 1 {
		t.Fatalf("stored %v", inner.ttls)
	}
	for key, ttl := range inner.ttls {
		if ttl != partialBucketTTL || !strings.Contains(key, partialStatementKeyKind) {
			t.Errorf("%s stored for %s", key, ttl)
		}
	}
}

// blockingPartials answers a partial bucket's statement only when its context ends, and the rest as
// interior answers them
type blockingPartials struct {
	*fakeUpstream
	interior  func(query string) ([]byte, error)
	cancelled chan string
}

func (b *blockingPartials) Execute(ctx context.Context, query string) ([]byte, error) {
	if strings.Contains(query, ">= 30)") || strings.Contains(query, "< 630)") {
		select {
		case <-ctx.Done():
			b.cancelled <- query
			return nil, ctx.Err()
		case <-time.After(time.Minute):
			return nil, errors.New("the partial bucket was never cancelled")
		}
	}
	return b.interior(query)
}

func TestDeltaTierFallbacksNeverWaitForPartialBuckets(t *testing.T) {
	ranged := rangedUpstream(t)
	query := fmt.Sprintf(deltaQuery, 30, 630)
	for name, interior := range map[string]func(q string) ([]byte, error){
		// an unrepresentable interior falls to the object tier, which the client's own statement answers
		"object tier": func(q string) ([]byte, error) {
			if q == query {
				return ranged(q)
			}
			return unrepresentableIPC(t), nil
		},
		"origin error": func(string) ([]byte, error) { return nil, errors.New("origin down") },
	} {
		t.Run(name, func(t *testing.T) {
			up := &blockingPartials{fakeUpstream: &fakeUpstream{}, interior: interior, cancelled: make(chan string, 2)}
			srv, _ := partialTestServer(t, up.fakeUpstream, timeseries.StepAlignmentPartial)
			srv.upstream = up
			done := make(chan error, 1)
			go func() {
				_, ch, err := srv.DoGetStatement(context.Background(), fakeStatementTicket{handle: []byte(query)})
				if err == nil {
					for chunk := range ch {
						chunk.Data.Release()
					}
				}
				done <- err
			}()
			select {
			case err := <-done:
				if (name == "origin error") != (err != nil) {
					t.Fatalf("= %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the fallback waited for its partial buckets")
			}
			for range 2 {
				select {
				case q := <-up.cancelled:
					if !strings.Contains(q, ">= 30)") && !strings.Contains(q, "< 630)") {
						t.Fatalf("cancelled %q", q)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("a discarded partial bucket fetch was never cancelled")
				}
			}
		})
	}
}

// overlappingPartials answers the interior only once a partial bucket's fetch has begun
type overlappingPartials struct {
	*fakeUpstream
	answer  func(query string) ([]byte, error)
	started chan struct{}
	once    sync.Once
}

func (o *overlappingPartials) Execute(_ context.Context, query string) ([]byte, error) {
	if strings.Contains(query, ">= 30)") || strings.Contains(query, "< 630)") {
		o.once.Do(func() { close(o.started) })
		return o.answer(query)
	}
	select {
	case <-o.started:
	case <-time.After(5 * time.Second):
		return nil, errors.New("the partial buckets waited for the interior")
	}
	return o.answer(query)
}

func TestDeltaTierFetchesPartialBucketsBesideTheInterior(t *testing.T) {
	up := &overlappingPartials{fakeUpstream: &fakeUpstream{}, answer: bucketedUpstream(t), started: make(chan struct{})}
	srv, _ := partialTestServer(t, up.fakeUpstream, timeseries.StepAlignmentPartial)
	srv.upstream = up
	query := fmt.Sprintf(deltaQuery+" ORDER BY time, host", 30, 630)
	want := executeRows(t, NewServer(&fakeUpstream{executeFn: bucketedUpstream(t)}, nil), query)
	if got := executeRows(t, srv, query); !reflect.DeepEqual(got, want) {
		t.Fatalf("= %v, want %v", got, want)
	}
}
