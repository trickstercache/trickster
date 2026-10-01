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
	"io"
	"strconv"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	benchStep       = 5 * time.Minute
	benchSeries     = 3
	benchPanelRows  = 288 * benchSeries
	benchLargeRows  = 100000
	benchEpochStart = 1789776000
)

func benchDelta(buckets, series int, from time.Time) *nativedelta.Delta {
	// rows as the row sink models them: a series per host, each point one DataRow body
	builder := dataset.NewBuilder(nil, dataset.BuilderOptions{Fields: timeseries.SeriesFields{
		Tags:   timeseries.FieldDefinitions{{Name: fakeHostColumn}},
		Values: timeseries.FieldDefinitions{{Name: rowValue}},
	}})
	for bucket := range buckets {
		at := from.Add(time.Duration(bucket) * benchStep)
		for s := range series {
			host := []byte("series-" + strconv.Itoa(s))
			body, _ := (&pgproto3.DataRow{Values: [][]byte{
				[]byte(at.Format("2006-01-02 15:04:05+00")), host, []byte("12345.678"),
			}}).Encode(nil)
			row := builder.Row()
			row.SetEpoch(epoch.Epoch(at.UnixNano()))
			row.SetTag(0, host)
			row.AddBytes(body[frameHeaderLen:])
			if err := row.Commit(); err != nil {
				panic(err)
			}
		}
	}
	ds, err := builder.Finish()
	if err != nil {
		panic(err)
	}
	return &nativedelta.Delta{Header: []byte(resultTestDescription), DS: ds}
}

func BenchmarkHitPath(b *testing.B) {
	// a hit's cost after the lookup: crop the stored rows and encode the response; a partial hit also
	// merges and stores. Each must stay linear in rows.
	start := time.Unix(benchEpochStart, 0).UTC()
	plan := &sqlanalyzer.QueryPlan{OutputColumn: "time"}
	descendingPlan := &sqlanalyzer.QueryPlan{Ordering: []sqlanalyzer.OrderTerm{{Column: "time", Descending: true}}}
	for name, rows := range map[string]int{"panel": benchPanelRows, "large": benchLargeRows} {
		buckets := rows / benchSeries
		cached := benchDelta(buckets, benchSeries, start)
		requested := timeseries.Extent{
			Start: start.Add(benchStep * time.Duration(buckets/4)), End: start.Add(benchStep * time.Duration(3*buckets/4)),
		}
		// a bytes-cache hit after the lookup: decode the entry, crop it and encode the response
		cache := newByteCache()
		engine := nativedelta.New(nativedelta.Config{
			Protocol: "bench", CacheTTL: time.Hour, CacheClient: func() trickstercache.Cache { return cache },
		}, resultCodec{})
		engine.StoreDelta(name, &nativedelta.Entry[*nativedelta.Delta]{Payload: cached, Extents: cached.DS.ExtentList})
		// as a cached answer is written: through a pooled buffer to the client
		write := func(d *nativedelta.Delta, plan *sqlanalyzer.QueryPlan) {
			buffer := pumpBuffers.Get().(*[]byte)
			out := frameWriter{w: io.Discard, buffer: (*buffer)[:0]}
			writeDelta(&out, d, plan)
			out.flush()
			pumpBuffers.Put(buffer)
		}
		b.Run(name+"/bytes-hit", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				entry, ok := engine.RetrieveDelta(name)
				if !ok {
					b.Fatal("the entry was not cached")
				}
				write(&nativedelta.Delta{Header: entry.Payload.Header, DS: entry.Payload.DS.View(requested)}, plan)
			}
		})
		b.Run(name+"/crop", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cached.DS.View(requested)
			}
		})
		b.Run(name+"/encode", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				write(cached, plan)
			}
		})
		b.Run(name+"/encode-descending", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				write(cached, descendingPlan)
			}
		})
		tail := benchDelta(buckets/10+1, benchSeries, start.Add(benchStep*time.Duration(buckets)))
		b.Run(name+"/merge", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = dataset.MergeDisjoint(nil, cached.DS, tail.DS)
			}
		})
		b.Run(name+"/marshal", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := dataset.MarshalDataSet(cached.DS, nil, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
		stored, _ := dataset.MarshalDataSet(cached.DS, nil, 0)
		b.Run(name+"/unmarshal", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(stored)))
			for b.Loop() {
				if _, err := dataset.UnmarshalDataSet(stored, nil); err != nil {
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
