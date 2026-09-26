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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	benchStep       = 5 * time.Minute
	benchSeries     = 3
	benchPanelRows  = 288 * benchSeries
	benchLargeRows  = 100000
	benchEpochStart = 1789776000
)

func benchResult(buckets, series int, from time.Time) *Result {
	r := &Result{RowDescription: []byte(resultTestDescription), times: []int64{}}
	for bucket := range buckets {
		at := from.Add(time.Duration(bucket) * benchStep)
		for s := range series {
			body, _ := (&pgproto3.DataRow{Values: [][]byte{
				[]byte(at.Format("2006-01-02 15:04:05+00")), []byte("series-" + strconv.Itoa(s)), []byte("12345.678"),
			}}).Encode(nil)
			r.appendRow(body[frameHeaderLen:], at.UnixNano(), true)
		}
	}
	return r
}

func BenchmarkHitPath(b *testing.B) {
	// what a cache hit costs after the lookup: decode the stored object, crop it to the
	// request, and encode the wire response. Each must stay linear in rows.
	start := time.Unix(benchEpochStart, 0).UTC()
	for name, rows := range map[string]int{"panel": benchPanelRows, "large": benchLargeRows} {
		buckets := rows / benchSeries
		cached := benchResult(buckets, benchSeries, start)
		stored, err := resultCodec{}.Marshal(cached)
		if err != nil {
			b.Fatal(err)
		}
		requested := timeseries.Extent{
			Start: start.Add(benchStep * time.Duration(buckets/4)), End: start.Add(benchStep * time.Duration(3*buckets/4)),
		}
		b.Run(name+"/unmarshal", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(stored)))
			for b.Loop() {
				if _, err := (resultCodec{}).Unmarshal(stored); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/crop", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cached.crop(requested)
			}
		})
		b.Run(name+"/encode", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cached.encode(false)
			}
		})
		b.Run(name+"/encode-descending", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cached.encode(true)
			}
		})
		// a partial hit merges what was cached with the newly fetched tail and stores the result
		tail := benchResult(buckets/10+1, benchSeries, start.Add(benchStep*time.Duration(buckets-1)))
		b.Run(name+"/merge", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := mergeResults([]*Result{cached, tail}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/marshal", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := (resultCodec{}).Marshal(cached); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGate(b *testing.B) {
	// the per-statement cost added to a relayed query: classify, analyze, derive the key
	for name, sql := range map[string]string{"delta": gateDeltaQuery, "object": gateObjectQuery, "keepalive": fakeQueryPing} {
		body := append([]byte(sql), 0)
		b.Run(name, func(b *testing.B) {
			s := gateTestSessionB(b)
			b.ReportAllocs()
			for b.Loop() {
				_ = s.gateQuery(body)
			}
		})
	}
}

func gateTestSessionB(b *testing.B) *session {
	b.Helper()
	server, err := NewServer(Config{
		BackendName: b.Name(), Dialect: testProvider, Upstream: Upstream{Address: testUnusedAddress},
		Analyzer: testAnalyzer, MaxQuerySizeBytes: 1 << 20, MaxMessageSizeBytes: 1 << 20,
	})
	if err != nil {
		b.Fatal(err)
	}
	s := &session{front: server, server: server, user: testClientUser, database: testDatabase}
	s.tracker = newSessionTracker(s.user, s.database, nil)
	s.txStatus.Store(txStatusIdle)
	return s
}
