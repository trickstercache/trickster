/*
 * Copyright 2026 The Trickster Authors
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

package mysql

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/vt/sqlparser"
)

type compatibilityCorpus struct {
	SchemaVersion   int                 `json:"schema_version"`
	CorpusVersion   string              `json:"corpus_version"`
	MinimumInterval string              `json:"minimum_interval"`
	Omissions       []string            `json:"omissions"`
	Cases           []compatibilityCase `json:"cases"`
}

type compatibilityCase struct {
	Name           string                `json:"name"`
	MacroSource    []string              `json:"macro_source"`
	QueryOrigin    string                `json:"query_origin"`
	GrafanaVersion string                `json:"grafana_version"`
	ExpandedSQL    string                `json:"expanded_sql"`
	Expected       compatibilityExpected `json:"expected"`
	Rationale      string                `json:"rationale"`
}

type compatibilityExpected struct {
	CacheMode         string   `json:"cache_mode"`
	AnalysisReason    string   `json:"analysis_reason"`
	Cadence           string   `json:"cadence"`
	Phase             string   `json:"phase"`
	InputUnit         string   `json:"input_unit"`
	OutputUnit        string   `json:"output_unit"`
	LowerBound        string   `json:"lower_bound"`
	LowerInclusive    bool     `json:"lower_inclusive"`
	UpperBound        string   `json:"upper_bound"`
	UpperInclusive    bool     `json:"upper_inclusive"`
	OutputColumn      string   `json:"output_column"`
	GroupColumns      []string `json:"group_columns"`
	CanonicalPolicy   string   `json:"canonical_policy"`
	ExtentRendering   bool     `json:"extent_rendering"`
	CanonicalContains []string `json:"canonical_contains"`
	CanonicalExcludes []string `json:"canonical_excludes"`
}

func loadCompatibilityCorpus(t testing.TB) compatibilityCorpus {
	t.Helper()
	data, err := os.ReadFile("testdata/compatibility/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus compatibilityCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func BenchmarkMySQLCompatibilityCorpus(b *testing.B) {
	corpus := loadCompatibilityCorpus(b)
	analyzer := mustNewAnalyzer()
	b.Run("Analyze", func(b *testing.B) {
		for _, tc := range corpus.Cases {
			b.Run(tc.Expected.CacheMode+"/"+tc.Name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = analyzer.Analyze(tc.ExpandedSQL, time.Time{})
				}
			})
		}
		for name, query := range map[string]string{
			"invalid_sql":         "SELECT FROM telemetry",
			"complex_unsupported": "WITH recent AS (SELECT * FROM telemetry) SELECT * FROM recent UNION SELECT * FROM telemetry",
		} {
			b.Run("none/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_ = analyzer.Analyze(query, time.Time{})
				}
			})
		}
	})
	b.Run("ParseFormatParse", func(b *testing.B) {
		for _, tc := range corpus.Cases {
			b.Run(tc.Name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					first, err := analyzer.parser.Parse(tc.ExpandedSQL)
					if err != nil {
						b.Fatal(err)
					}
					formatted := sqlparser.String(first)
					if _, err := analyzer.parser.Parse(formatted); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	})
	for _, tc := range corpus.Cases {
		analysis := analyzer.Analyze(tc.ExpandedSQL, time.Time{})
		if analysis.Plan == nil {
			continue
		}
		plan := analysis.Plan
		full := timeseries.Extent{
			Start: plan.LowerBound.Value,
			End:   plan.UpperBound.Value.Add(-plan.Step),
		}
		partial := timeseries.Extent{Start: full.Start.Add(plan.Step), End: full.Start.Add(2 * plan.Step)}
		b.Run("RenderFull/"+tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := plan.RenderExtent(full); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("RenderPartialHit/"+tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := plan.RenderExtent(partial); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("RenderConcurrent/"+tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := plan.RenderExtent(partial); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkMySQLSmoke(b *testing.B) {
	analyzer := mustNewAnalyzer()
	analysis := analyzer.Analyze(safeDateTimeQuery, time.Time{})
	if analysis.Plan == nil {
		b.Fatal("smoke query did not produce a DPC plan")
	}
	extent := timeseries.Extent{
		Start: analysis.Plan.LowerBound.Value,
		End:   analysis.Plan.LowerBound.Value.Add(analysis.Plan.Step),
	}
	cached := &cachedQueryResult{result: benchmarkResult(10)}
	b.ReportAllocs()
	for b.Loop() {
		if analyzer.Analyze(safeDateTimeQuery, time.Time{}).Plan == nil {
			b.Fatal("analysis failed")
		}
		if _, err := analysis.Plan.RenderExtent(extent); err != nil {
			b.Fatal(err)
		}
		encoded, err := marshalCachedQueryResult(cached)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := unmarshalCachedQueryResult(encoded); err != nil {
			b.Fatal(err)
		}
	}
}

func TestMySQLCompatibilityCorpus(t *testing.T) {
	corpus := loadCompatibilityCorpus(t)
	if corpus.SchemaVersion != 1 || corpus.CorpusVersion == "" {
		t.Fatalf("invalid corpus version: %+v", corpus)
	}
	if corpus.MinimumInterval != "1m" {
		t.Fatalf("minimum_interval = %q, want 1m", corpus.MinimumInterval)
	}
	for _, omission := range []string{
		"dashboard-query-inspector-capture", "builder-and-code-mode-pairs",
	} {
		if !slices.Contains(corpus.Omissions, omission) {
			t.Fatalf("corpus does not record approved omission %q", omission)
		}
	}

	seen := make(map[string]struct{}, len(corpus.Cases))
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Name == "" || tc.ExpandedSQL == "" || tc.Rationale == "" ||
				tc.Expected.CanonicalPolicy == "" ||
				(len(tc.MacroSource) == 0 && tc.QueryOrigin == "") {
				t.Fatalf("case lacks required documentation: %+v", tc)
			}
			if strings.Contains(strings.Join(tc.MacroSource, " "), "$__") &&
				tc.GrafanaVersion == "" {
				t.Fatalf("Grafana macro case lacks grafana_version: %+v", tc)
			}
			if _, ok := seen[tc.Name]; ok {
				t.Fatalf("duplicate compatibility case %q", tc.Name)
			}
			seen[tc.Name] = struct{}{}

			analysis := defaultAnalyzer.Analyze(tc.ExpandedSQL, time.Time{})
			wantMode := compatibilityMode(t, tc.Expected.CacheMode)
			wantReason := sqlanalyzer.AnalysisReason(tc.Expected.AnalysisReason)
			if analysis.Mode != wantMode || analysis.Reason != wantReason {
				t.Fatalf("Analyze() = %s/%s (%v), want %s/%s", analysis.Mode,
					analysis.Reason, analysis.Err, wantMode, wantReason)
			}
			if wantMode != sqlanalyzer.CacheModeDelta {
				if analysis.Plan != nil || tc.Expected.ExtentRendering ||
					tc.Expected.CanonicalPolicy != "none" {
					t.Fatalf("non-DPC case unexpectedly has a renderable plan: %+v", analysis.Plan)
				}
				return
			}
			assertCompatibilityPlan(t, analysis.Plan, tc.Expected)
		})
	}
}

func TestCompatibilityCorpusCoversGrafanaMacros(t *testing.T) {
	corpus := loadCompatibilityCorpus(t)
	allSources := strings.Join(func() []string {
		out := make([]string, 0, len(corpus.Cases))
		for _, tc := range corpus.Cases {
			out = append(out, tc.MacroSource...)
		}
		return out
	}(), "\n")
	for _, macro := range []string{
		"$__time(", "$__timeEpoch(", "$__timeFilter(", "$__timeFrom(", "$__timeTo(",
		"$__timeGroup(", "$__timeGroupAlias(", "$__unixEpochFilter(",
		"$__unixEpochFrom(", "$__unixEpochTo(", "$__unixEpochGroup(",
		"$__unixEpochGroupAlias(", "$__interval",
	} {
		if !strings.Contains(allSources, macro) {
			t.Errorf("compatibility corpus does not cover %s", macro)
		}
	}
}

func TestCompatibilityCorpusParseFormatParseAndRenderStability(t *testing.T) {
	corpus := loadCompatibilityCorpus(t)
	analyzer := mustNewAnalyzer()
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			first, err := analyzer.parser.Parse(tc.ExpandedSQL)
			if err != nil {
				t.Fatalf("initial parse: %v", err)
			}
			formatted := sqlparser.String(first)
			second, err := analyzer.parser.Parse(formatted)
			if err != nil {
				t.Fatalf("formatted parse: %v\n%s", err, formatted)
			}
			if reformatted := sqlparser.String(second); reformatted != formatted {
				t.Fatalf("parse-format-parse is unstable:\n%s\n%s", formatted, reformatted)
			}

			original := analyzer.Analyze(tc.ExpandedSQL, time.Time{})
			roundTrip := analyzer.Analyze(formatted, time.Time{})
			if original.Mode != roundTrip.Mode || original.Reason != roundTrip.Reason {
				t.Fatalf("format changed analysis from %s/%s to %s/%s",
					original.Mode, original.Reason, roundTrip.Mode, roundTrip.Reason)
			}
			if original.Mode != sqlanalyzer.CacheModeDelta {
				return
			}
			if original.Plan.CanonicalSQL != roundTrip.Plan.CanonicalSQL {
				t.Fatalf("format changed canonical identity:\n%s\n%s",
					original.Plan.CanonicalSQL, roundTrip.Plan.CanonicalSQL)
			}
			rendered, err := original.Plan.RenderExtent(timeseries.Extent{
				Start: original.Plan.LowerBound.Value,
				End:   original.Plan.LowerBound.Value.Add(original.Plan.Step),
			})
			if err != nil {
				t.Fatalf("RenderExtent(): %v", err)
			}
			renderedAnalysis := analyzer.Analyze(rendered, time.Time{})
			if renderedAnalysis.Mode != sqlanalyzer.CacheModeDelta ||
				renderedAnalysis.Plan.CanonicalSQL != original.Plan.CanonicalSQL {
				t.Fatalf("render changed a non-bound expression:\noriginal: %s\nrendered: %s",
					original.Plan.CanonicalSQL, rendered)
			}
		})
	}
}

func compatibilityMode(t *testing.T, value string) sqlanalyzer.CacheMode {
	t.Helper()
	switch value {
	case "none":
		return sqlanalyzer.CacheModeNone
	case "object":
		return sqlanalyzer.CacheModeObject
	case "delta":
		return sqlanalyzer.CacheModeDelta
	default:
		t.Fatalf("unknown cache mode %q", value)
		return sqlanalyzer.CacheModeNone
	}
}

func assertCompatibilityPlan(t *testing.T, plan *sqlanalyzer.QueryPlan,
	want compatibilityExpected,
) {
	t.Helper()
	if plan == nil {
		t.Fatal("DPC case has no query plan")
	}
	if want.CanonicalPolicy != "range_independent" {
		t.Fatalf("DPC canonical policy = %q, want range_independent", want.CanonicalPolicy)
	}
	step, err := time.ParseDuration(want.Cadence)
	if err != nil {
		t.Fatalf("invalid cadence %q: %v", want.Cadence, err)
	}
	phase, err := time.ParseDuration(want.Phase)
	if err != nil {
		t.Fatalf("invalid phase %q: %v", want.Phase, err)
	}
	lower, err := time.Parse(time.RFC3339Nano, want.LowerBound)
	if err != nil {
		t.Fatalf("invalid lower bound %q: %v", want.LowerBound, err)
	}
	upper, err := time.Parse(time.RFC3339Nano, want.UpperBound)
	if err != nil {
		t.Fatalf("invalid upper bound %q: %v", want.UpperBound, err)
	}
	if plan.Step != step || plan.Phase != phase ||
		plan.InputUnit != compatibilityUnit(t, want.InputUnit) ||
		plan.OutputUnit != compatibilityUnit(t, want.OutputUnit) ||
		plan.OutputColumn != want.OutputColumn || !slices.Equal(plan.GroupColumns, want.GroupColumns) {
		t.Fatalf("plan facts = %+v, want %+v", plan, want)
	}
	if plan.LowerBound == nil || plan.UpperBound == nil ||
		!plan.LowerBound.Value.Equal(lower) || plan.LowerBound.Inclusive != want.LowerInclusive ||
		!plan.UpperBound.Value.Equal(upper) || plan.UpperBound.Inclusive != want.UpperInclusive {
		t.Fatalf("plan bounds = %+v/%+v, want %s/%s", plan.LowerBound,
			plan.UpperBound, want.LowerBound, want.UpperBound)
	}
	for _, fragment := range want.CanonicalContains {
		if !strings.Contains(plan.CanonicalSQL, fragment) {
			t.Errorf("canonical SQL lacks %q: %s", fragment, plan.CanonicalSQL)
		}
	}
	for _, fragment := range want.CanonicalExcludes {
		if strings.Contains(plan.CanonicalSQL, fragment) {
			t.Errorf("canonical SQL retained range literal %q: %s", fragment, plan.CanonicalSQL)
		}
	}
	if !want.ExtentRendering {
		t.Fatal("DPC corpus case must require extent rendering")
	}
	rendered, err := plan.RenderExtent(timeseries.Extent{
		Start: lower, End: lower.Add(step),
	})
	if err != nil {
		t.Fatalf("RenderExtent(): %v", err)
	}
	if strings.Contains(rendered, "__trickster_mysql_") {
		t.Fatalf("rendered SQL retained internal placeholders: %s", rendered)
	}
}

func compatibilityUnit(t *testing.T, value string) timeseries.FieldDataType {
	t.Helper()
	switch value {
	case "datetime_sql":
		return timeseries.DateTimeSQL
	case "unix_seconds":
		return timeseries.DateTimeUnixSecs
	case "unix_nanoseconds":
		return timeseries.DateTimeUnixNano
	default:
		t.Fatalf("unknown time unit %q", value)
		return timeseries.Unknown
	}
}
