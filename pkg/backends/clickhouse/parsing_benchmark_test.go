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

package clickhouse

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// BenchmarkClickHouseParseAndTokenize matches the end-to-end benchmark used
// by the generated-AST implementation: parse a representative query, analyze
// its cache eligibility, extract its extent, and produce its tokenized form.
func BenchmarkClickHouseParseAndTokenize(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		trq, _, _, err := parse(tq03, nil)
		if err != nil {
			b.Fatal(err)
		}
		if trq.Statement == "" {
			b.Fatal("empty tokenized statement")
		}
	}
}

func BenchmarkClickHouseRenderExtent(b *testing.B) {
	trq, _, _, err := parse(tq03, nil)
	if err != nil {
		b.Fatal(err)
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok {
		b.Fatal("missing query plan")
	}
	extent := timeseries.Extent{
		Start: time.Unix(1516665600, 0),
		End:   time.Unix(1516687200, 0),
	}
	b.ReportAllocs()
	for b.Loop() {
		rendered, err := plan.RenderExtent(extent)
		if err != nil {
			b.Fatal(err)
		}
		if rendered == "" {
			b.Fatal("empty rendered statement")
		}
	}
}
