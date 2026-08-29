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
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// deltaTestCache adapts the package's memCache-style store to the trickster
// cache interface the delta engine consumes.
type deltaTestCache struct{ inner *memCache }

func (c deltaTestCache) Connect() error { return nil }
func (c deltaTestCache) Close() error   { return nil }
func (c deltaTestCache) Store(key string, data []byte, ttl time.Duration) error {
	c.inner.Set(key, data, ttl)
	return nil
}

func (c deltaTestCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	if data, ok := c.inner.Get(key); ok {
		return data, status.LookupStatusHit, nil
	}
	return nil, status.LookupStatusKeyMiss, trickstercache.ErrKNF
}

func (c deltaTestCache) Remove(keys ...string) error {
	c.inner.mu.Lock()
	for _, key := range keys {
		delete(c.inner.data, key)
	}
	c.inner.mu.Unlock()
	return nil
}

func (c deltaTestCache) Configuration() *cacheoptions.Options { return cacheoptions.New() }

var testAnalyzer = cockroach.NewAnalyzer(cockroach.Options{
	BucketMatchers:           cockroach.DataFusionBucketMatchers(),
	RoundUnalignedTimeBounds: true,
})

var renderedBounds = regexp.MustCompile(`>= (\d+).* < (\d+)`)

// rangedUpstream serves one row per minute bucket per host for whatever time
// range the rendered statement names.
func rangedUpstream(t testing.TB) func(query string) ([]byte, error) {
	t.Helper()
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
		for at := lower; at < upper; at += 60 {
			for _, host := range []string{"a", "b"} {
				builder.Field(0).(*array.TimestampBuilder).Append(
					arrow.Timestamp(at * int64(time.Second)))
				builder.Field(1).(*array.StringBuilder).Append(host)
				builder.Field(2).(*array.Float64Builder).Append(float64(at))
			}
		}
		record := builder.NewRecordBatch()
		defer record.Release()
		return EncodeRecords(schema, []arrow.RecordBatch{record})
	}
}

func newDeltaTestServer(t *testing.T, up *fakeUpstream) *Server {
	t.Helper()
	inner := newMemCache()
	return NewServer(up, inner,
		WithCacheKeyPrefix("influx3"),
		WithDeltaCache(DeltaConfig{
			Analyzer:    testAnalyzer,
			CacheClient: func() trickstercache.Cache { return deltaTestCache{inner: inner} },
			CacheTTL:    time.Hour,
		}))
}

func executeRows(t *testing.T, srv *Server, query string) [][]any {
	t.Helper()
	_, ch, err := srv.DoGetStatement(context.Background(),
		fakeStatementTicket{handle: []byte(query)})
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	for chunk := range ch {
		record := chunk.Data
		for row := range int(record.NumRows()) {
			cells := make([]any, record.NumCols())
			for i := range int(record.NumCols()) {
				column := record.Column(i)
				switch typed := column.(type) {
				case *array.Timestamp:
					cells[i] = int64(typed.Value(row))
				case *array.String:
					cells[i] = typed.Value(row)
				case *array.Float64:
					cells[i] = typed.Value(row)
				default:
					cells[i] = column.ValueStr(row)
				}
			}
			rows = append(rows, cells)
		}
		record.Release()
	}
	return rows
}

const deltaQuery = "SELECT date_bin(INTERVAL '1 minute', time) AS time, host, avg(v) AS v " +
	"FROM m WHERE time >= %d AND time < %d GROUP BY 1, host"

func TestDeltaTierCachesByExtent(t *testing.T) {
	up := &fakeUpstream{executeFn: rangedUpstream(t)}
	srv := newDeltaTestServer(t, up)

	first := executeRows(t, srv, fmt.Sprintf(deltaQuery, 0, 600))
	if up.executeCalls != 1 {
		t.Fatalf("full miss made %d upstream calls", up.executeCalls)
	}
	// 10 buckets x 2 hosts
	if len(first) != 20 {
		t.Fatalf("first response rows = %d, want 20", len(first))
	}

	second := executeRows(t, srv, fmt.Sprintf(deltaQuery, 0, 600))
	if up.executeCalls != 1 {
		t.Fatalf("full hit made %d upstream calls", up.executeCalls)
	}
	if len(second) != len(first) {
		t.Fatalf("cached response rows = %d, want %d", len(second), len(first))
	}

	// widening the window fetches only the missing tail
	widened := executeRows(t, srv, fmt.Sprintf(deltaQuery, 0, 1200))
	if up.executeCalls != 2 {
		t.Fatalf("partial hit made %d upstream calls", up.executeCalls)
	}
	if len(widened) != 40 {
		t.Fatalf("widened response rows = %d, want 40", len(widened))
	}
	tail := up.executedQueries[len(up.executedQueries)-1]
	if match := renderedBounds.FindStringSubmatch(tail); match == nil || match[1] != "600" {
		t.Fatalf("delta fetch did not start at the missing extent: %s", tail)
	}
	// time-major ordering with both hosts per bucket
	if widened[0][1] != "a" || widened[1][1] != "b" ||
		widened[0][0] != int64(0) || widened[39][0] != 1140*int64(time.Second) {
		t.Fatalf("widened rows misordered: first=%v last=%v", widened[0], widened[39])
	}
}

func TestDeltaTierRoutesNonDeltaStatements(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	srv := newDeltaTestServer(t, up)

	// nondeterministic: never cached
	executeRows(t, srv, "SELECT now(), * FROM cpu")
	executeRows(t, srv, "SELECT now(), * FROM cpu")
	if up.executeCalls != 2 {
		t.Fatalf("volatile statement made %d upstream calls, want 2", up.executeCalls)
	}

	// analyzable but not delta-cacheable: object tier
	up.executeCalls = 0
	executeRows(t, srv, "SELECT * FROM cpu LIMIT 5")
	executeRows(t, srv, "SELECT * FROM cpu LIMIT 5")
	if up.executeCalls != 1 {
		t.Fatalf("object-tier statement made %d upstream calls, want 1", up.executeCalls)
	}
}

func TestDeltaTierFallsBackOnUnrepresentableSchema(t *testing.T) {
	// the upstream returns a list-typed column the dataset model cannot express
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: &arrow.TimestampType{Unit: arrow.Nanosecond}},
		{Name: "l", Type: arrow.ListOf(arrow.PrimitiveTypes.Int64)},
	}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	record := builder.NewRecordBatch()
	ipcBytes, err := EncodeRecords(schema, []arrow.RecordBatch{record})
	record.Release()
	builder.Release()
	if err != nil {
		t.Fatal(err)
	}
	up := &fakeUpstream{ipcBytes: ipcBytes}
	srv := newDeltaTestServer(t, up)

	query := fmt.Sprintf(deltaQuery, 0, 600)
	first := executeRows(t, srv, query)
	if len(first) != 0 {
		t.Fatalf("unexpected rows: %v", first)
	}
	// unrepresentable → delta fetch discarded, object tier fetched the
	// original (2 calls); the fallback marker then routes the second request
	// straight to the object tier, which hits its cache (0 further calls)
	callsAfterFirst := up.executeCalls
	if callsAfterFirst != 2 {
		t.Fatalf("fallback made %d upstream calls, want 2", callsAfterFirst)
	}
	executeRows(t, srv, query)
	if up.executeCalls != callsAfterFirst {
		t.Fatalf("marker did not short-circuit: %d calls", up.executeCalls)
	}
}

func TestDeltaTierOpenEndedWindowExcludesVolatileTail(t *testing.T) {
	up := &fakeUpstream{executeFn: rangedUpstream(t)}
	srv := newDeltaTestServer(t, up)

	lower := time.Now().Add(-10 * time.Minute).Truncate(time.Minute).Unix()
	query := fmt.Sprintf("SELECT date_bin(INTERVAL '1 minute', time) AS time, host, avg(v) AS v "+
		"FROM m WHERE time >= %d GROUP BY 1, host", lower)
	first := executeRows(t, srv, query)
	if up.executeCalls != 1 || len(first) == 0 {
		t.Fatalf("open-ended miss = %d calls, %d rows", up.executeCalls, len(first))
	}
	// the still-filling tail is excluded from storage, so an immediate rerun
	// refetches only the volatile buckets
	second := executeRows(t, srv, query)
	if up.executeCalls != 2 {
		t.Fatalf("open-ended rerun made %d upstream calls, want 2", up.executeCalls)
	}
	if len(second) < len(first)-4 || len(second) > len(first)+4 {
		t.Fatalf("open-ended rerun rows = %d vs %d", len(second), len(first))
	}
	tail := up.executedQueries[len(up.executedQueries)-1]
	match := renderedBounds.FindStringSubmatch(tail)
	if match == nil {
		t.Fatalf("no bounds in volatile refetch %q", tail)
	}
	refetchLower, _ := strconv.ParseInt(match[1], 10, 64)
	if refetchLower <= lower {
		t.Fatalf("volatile refetch re-fetched the whole window: %s", tail)
	}
}

// TestDeltaTierPreservesOrderBy verifies that a delta hit, whose rows are
// rebuilt from the cached dataset, returns them in the statement's ORDER BY
// order rather than the model's time-major default.
func TestDeltaTierPreservesOrderBy(t *testing.T) {
	const orderedQuery = deltaQuery + " ORDER BY time DESC, host DESC"
	up := &fakeUpstream{executeFn: rangedUpstream(t)}
	srv := newDeltaTestServer(t, up)

	miss := executeRows(t, srv, fmt.Sprintf(orderedQuery, 0, 300))
	if up.executeCalls != 1 {
		t.Fatalf("full miss made %d upstream calls", up.executeCalls)
	}
	hit := executeRows(t, srv, fmt.Sprintf(orderedQuery, 0, 300))
	if up.executeCalls != 1 {
		t.Fatalf("full hit made %d upstream calls", up.executeCalls)
	}
	if len(miss) != 10 || len(hit) != 10 {
		t.Fatalf("rows = %d/%d, want 10 each", len(miss), len(hit))
	}
	want := [][]any{
		{240 * int64(time.Second), "b"}, {240 * int64(time.Second), "a"},
		{180 * int64(time.Second), "b"}, {180 * int64(time.Second), "a"},
		{120 * int64(time.Second), "b"}, {120 * int64(time.Second), "a"},
		{60 * int64(time.Second), "b"}, {60 * int64(time.Second), "a"},
		{int64(0), "b"}, {int64(0), "a"},
	}
	for i, row := range want {
		if hit[i][0] != row[0] || hit[i][1] != row[1] {
			t.Fatalf("row %d = %v, want %v", i, hit[i][:2], row)
		}
	}
}

// TestDeltaTierOrderByAscending covers the ascending case, where the requested
// order matches the reconstruction's default but must still be honored after a
// partial hit merges two fetched ranges.
func TestDeltaTierOrderByAscending(t *testing.T) {
	const orderedQuery = deltaQuery + " ORDER BY time"
	up := &fakeUpstream{executeFn: rangedUpstream(t)}
	srv := newDeltaTestServer(t, up)

	executeRows(t, srv, fmt.Sprintf(orderedQuery, 0, 300))
	widened := executeRows(t, srv, fmt.Sprintf(orderedQuery, 0, 600))
	if up.executeCalls != 2 {
		t.Fatalf("partial hit made %d upstream calls, want 2", up.executeCalls)
	}
	if len(widened) != 20 {
		t.Fatalf("widened rows = %d, want 20", len(widened))
	}
	for i := 1; i < len(widened); i++ {
		if widened[i][0].(int64) < widened[i-1][0].(int64) {
			t.Fatalf("row %d is out of ascending order: %v after %v",
				i, widened[i], widened[i-1])
		}
	}
}
