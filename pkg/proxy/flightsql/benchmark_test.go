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
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
)

// The tier benchmarks compare the per-request serving cost of the three
// statement cache tiers at different response sizes, all with a warm cache
// and a fake in-process upstream so upstream latency is excluded:
//
//   - delta_hit: the delta tier rebuilds Arrow record batches from the cached
//     dataset (the response-shaping cost of extent-based caching);
//   - object_hit: the object tier streams the cached IPC bytes verbatim;
//   - passthrough: an uncacheable statement re-fetched from the (fake)
//     upstream on every call.
//
// Row counts are minutes x 2 hosts. See docs/developer/flightsql-benchmarks.md
// for the recorded results and analysis.

func drainStatement(tb testing.TB, srv *Server, query string) int {
	tb.Helper()
	_, ch, err := srv.DoGetStatement(context.Background(),
		fakeStatementTicket{handle: []byte(query)})
	if err != nil {
		tb.Fatalf("DoGetStatement: %v", err)
	}
	rows := 0
	for chunk := range ch {
		rows += int(chunk.Data.NumRows())
		chunk.Data.Release()
	}
	return rows
}

func BenchmarkFlightSQLStatementTiers(b *testing.B) {
	for _, minutes := range []int{50, 500, 5000} {
		rows := minutes * 2
		up := &fakeUpstream{executeFn: rangedUpstream(b)}
		inner := newMemCache()
		srv := NewServer(up, inner,
			WithCacheKeyPrefix("bench"),
			WithDeltaCache(DeltaConfig{
				Analyzer:    testAnalyzer,
				CacheClient: func() trickstercache.Cache { return deltaTestCache{inner: inner} },
				CacheTTL:    time.Hour,
			}))

		deltaStatement := fmt.Sprintf(deltaQuery, 0, minutes*60)
		if got := drainStatement(b, srv, deltaStatement); got != rows {
			b.Fatalf("delta prime rows = %d, want %d", got, rows)
		}
		b.Run(fmt.Sprintf("delta_hit/rows=%d", rows), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				drainStatement(b, srv, deltaStatement)
			}
		})

		// the object statement serves a canned payload of the same size
		canned, err := rangedUpstream(b)(fmt.Sprintf("t >= %d AND t < %d", 0, minutes*60))
		if err != nil {
			b.Fatal(err)
		}
		up.executeFn = nil
		up.ipcBytes = canned
		objectStatement := "SELECT * FROM cpu LIMIT 5"
		drainStatement(b, srv, objectStatement)
		b.Run(fmt.Sprintf("object_hit/rows=%d", rows), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				drainStatement(b, srv, objectStatement)
			}
		})

		b.Run(fmt.Sprintf("passthrough/rows=%d", rows), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				drainStatement(b, srv, "SELECT now(), * FROM cpu")
			}
		})
	}
}

// BenchmarkFlightSQLSmoke is the deterministic single-iteration benchmark CI
// compiles and runs to keep the benchmark package healthy.
func BenchmarkFlightSQLSmoke(b *testing.B) {
	up := &fakeUpstream{executeFn: rangedUpstream(b)}
	inner := newMemCache()
	srv := NewServer(up, inner,
		WithCacheKeyPrefix("smoke"),
		WithDeltaCache(DeltaConfig{
			Analyzer:    testAnalyzer,
			CacheClient: func() trickstercache.Cache { return deltaTestCache{inner: inner} },
			CacheTTL:    time.Hour,
		}))
	statement := fmt.Sprintf(deltaQuery, 0, 600)
	b.ReportAllocs()
	for b.Loop() {
		if rows := drainStatement(b, srv, statement); rows != 20 {
			b.Fatalf("smoke rows = %d, want 20", rows)
		}
	}
}
