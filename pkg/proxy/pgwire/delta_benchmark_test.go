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

package pgwire

import (
	"context"
	"fmt"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	deltaBenchFrom   = "2026-09-10T00:00:00Z"
	deltaBenchMiddle = "2026-09-10T12:00:00Z"
	deltaBenchTo     = "2026-09-11T00:00:00Z"
	// both ends inside a bucket, so the partial modes fetch an edge bucket at each
	deltaBenchEdgeFrom = "2026-09-10T00:02:00Z"
	deltaBenchEdgeTo   = "2026-09-10T23:58:00Z"
)

func deltaBenchQuery(tenant int, from, to string) string {
	// a day of five-minute buckets for two hosts, 576 rows; each tenant is its own cache entry
	return fmt.Sprintf("%sWHERE ts >= '%s' AND ts < '%s' AND tenant = %d GROUP BY 1, host ORDER BY 1",
		cacheTestHosts, from, to, tenant)
}

func BenchmarkDeltaCache(b *testing.B) {
	if a := (testEngine{}).Analyzer().Analyze(deltaBenchQuery(0, deltaBenchFrom, deltaBenchTo), time.Now()); a.Mode != sqlanalyzer.CacheModeDelta {
		b.Fatalf("the benchmark's statement is %s (%s), not a delta plan", a.Mode, a.Reason)
	}
	for _, kind := range []string{"memory", "bytes"} {
		b.Run(kind, func(b *testing.B) {
			upstream := newFakeUpstream(b, nil)
			config := cachedConfig(b, upstream)
			if kind == "memory" {
				config.Cache = benchMemoryCache(b)
			}
			_, address := startServer(b, config)
			conn := mustDial(b, address, testClientUser, testClientPass)
			run := func(sql string) {
				ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
				defer cancel()
				if _, err := conn.Exec(ctx, sql).ReadAll(); err != nil {
					b.Fatal(err)
				}
			}
			hit := deltaBenchQuery(0, deltaBenchFrom, deltaBenchTo)
			run(hit)
			upstream.forget()
			b.Run("Hit", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					run(hit)
				}
			})
			if sent := upstream.received(); len(sent) != 0 {
				b.Fatalf("a hit reached the origin: %q", sent)
			}
			b.Run("PartialHit", func(b *testing.B) {
				b.ReportAllocs()
				for i := 1; b.Loop(); i++ {
					b.StopTimer()
					run(deltaBenchQuery(i, deltaBenchFrom, deltaBenchMiddle))
					b.StartTimer()
					run(deltaBenchQuery(i, deltaBenchFrom, deltaBenchTo))
				}
			})
			b.Run("Miss", func(b *testing.B) {
				b.ReportAllocs()
				for i := 1_000_000; b.Loop(); i++ {
					run(deltaBenchQuery(i, deltaBenchFrom, deltaBenchTo))
				}
			})
			upstream.forget()
			b.Run("PartialBucketsHit", func(b *testing.B) {
				// a hit under partial, whose two edge buckets both come from the object tier
				partial := cachedConfig(b, upstream)
				partial.StepAlignment = timeseries.StepAlignmentPartial
				if kind == "memory" {
					partial.Cache = benchMemoryCache(b)
				}
				_, address := startServer(b, partial)
				conn = mustDial(b, address, testClientUser, testClientPass)
				edges := deltaBenchQuery(0, deltaBenchEdgeFrom, deltaBenchEdgeTo)
				run(edges)
				upstream.forget()
				b.ReportAllocs()
				for b.Loop() {
					run(edges)
				}
				if sent := upstream.received(); len(sent) != 0 {
					b.Fatalf("a hit reached the origin: %q", sent)
				}
			})
		})
	}
}

func benchMemoryCache(b *testing.B) trickstercache.Cache {
	b.Helper()
	configuration := cacheoptions.New()
	configuration.Name, configuration.Provider = "pgwire-delta-benchmark", cacheproviders.Memory
	client := cachemanager.NewCache(cachememory.New(configuration.Name, configuration),
		cachemanager.CacheOptions{}, configuration)
	if err := client.Connect(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return client
}
