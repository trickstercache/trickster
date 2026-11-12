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

package pgfamily

import (
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlgolden"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const benchStatement = `SELECT date_bin(INTERVAL '1 minute', ts) AS b, host, region, avg(cpu) AS cpu, max(mem) AS mem ` +
	`FROM metrics WHERE ts >= '2026-01-01T00:00:00Z' AND ts < '2026-01-01T06:00:00Z' AND host IN ('a', 'b') ` +
	`AND region = 'us-east-1' GROUP BY b, host, region ORDER BY b`

func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseOne(benchStatement); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormat(b *testing.B) {
	parsed, err := ParseOne(benchStatement)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = ast.AsString(parsed)
	}
}

func BenchmarkAnalyze(b *testing.B) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1767250000, 0)
	b.ReportAllocs()
	for b.Loop() {
		if analysis := a.Analyze(benchStatement, now); analysis.Plan == nil {
			b.Fatal(analysis.Err)
		}
	}
}

const benchSampleBy = `SELECT ts, host, region, avg(cpu) AS cpu, max(mem) AS mem FROM metrics ` +
	`WHERE ts >= '2026-01-01T00:00:00Z' AND ts < '2026-01-01T06:00:00Z' AND host IN ('a', 'b') SAMPLE BY 5m ORDER BY ts`

func BenchmarkAnalyzeDialectClause(b *testing.B) {
	a := sampleByAnalyzer()
	now := time.Unix(1767250000, 0)
	b.ReportAllocs()
	for b.Loop() {
		if analysis := a.Analyze(benchSampleBy, now); analysis.Plan == nil {
			b.Fatal(analysis.Err)
		}
	}
}

func BenchmarkRenderExtent(b *testing.B) {
	analysis := newDataFusionAnalyzer().Analyze(benchStatement, time.Unix(1767250000, 0))
	if analysis.Plan == nil {
		b.Fatal(analysis.Err)
	}
	extent := timeseries.Extent{Start: time.Unix(1767225600, 0), End: time.Unix(1767229140, 0)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := analysis.Plan.RenderExtent(extent); err != nil {
			b.Fatal(err)
		}
	}
}

// analyses on many goroutines share pooled parsers and one plan; each must match a lone analysis
func TestAnalyzeIsConcurrent(t *testing.T) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1767250000, 0)
	statements := sqlgolden.Statements(t)
	want := make([]string, len(statements))
	for i, sql := range statements {
		if plan := a.Analyze(sql, now).Plan; plan != nil {
			want[i] = plan.CanonicalSQL
		}
	}
	shared := a.Analyze(benchStatement, now).Plan
	extent := timeseries.Extent{Start: time.Unix(1767225600, 0), End: time.Unix(1767229140, 0)}
	wantRender, err := shared.RenderExtent(extent)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < len(statements); i += workers {
				var got string
				if plan := a.Analyze(statements[i], now).Plan; plan != nil {
					got = plan.CanonicalSQL
				}
				if got != want[i] {
					t.Errorf("%q: got %q, want %q", statements[i], got, want[i])
				}
				if rendered, err := shared.AppendExtent(nil, extent); err != nil || string(rendered) != wantRender {
					t.Errorf("shared render: %q, %v", rendered, err)
				}
			}
		})
	}
	wg.Wait()
}
