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

// Package sqlcompat runs SQL dialect compatibility corpora in backend tests.
package sqlcompat

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	corpusMinimumInterval = "1m"
	corpusPolicyNone      = "none"
	corpusPolicyRange     = "range_independent"
	corpusPlaceholder     = "TRICKSTER_TS"
)

type compatibilityCorpus struct {
	SchemaVersion   int                 `json:"schema_version"`
	CorpusVersion   string              `json:"corpus_version"`
	MinimumInterval string              `json:"minimum_interval"`
	Omissions       []string            `json:"omissions"`
	Cases           []compatibilityCase `json:"cases"`
}

type compatibilityCase struct {
	Name            string                `json:"name"`
	MacroSource     []string              `json:"macro_source"`
	QueryOrigin     string                `json:"query_origin"`
	GrafanaVersion  string                `json:"grafana_version"`
	SessionTimeZone string                `json:"session_time_zone"`
	ExpandedSQL     string                `json:"expanded_sql"`
	Expected        compatibilityExpected `json:"expected"`
	Rationale       string                `json:"rationale"`
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
	OpenEnded         bool     `json:"open_ended"`
	OutputColumn      string   `json:"output_column"`
	GroupColumns      []string `json:"group_columns"`
	CanonicalPolicy   string   `json:"canonical_policy"`
	ExtentRendering   bool     `json:"extent_rendering"`
	CanonicalContains []string `json:"canonical_contains"`
	CanonicalExcludes []string `json:"canonical_excludes"`
}

var (
	corpusModes = map[string]sqlanalyzer.CacheMode{
		"none": sqlanalyzer.CacheModeNone, "object": sqlanalyzer.CacheModeObject, "delta": sqlanalyzer.CacheModeDelta,
	}
	corpusUnits = map[string]timeseries.FieldDataType{
		"timestamp": timeseries.DateTimeRFC3339Nano, "rfc3339": timeseries.DateTimeRFC3339,
		"datetime_sql": timeseries.DateTimeSQL, "unix_seconds": timeseries.DateTimeUnixSecs,
	}
)

func loadCompatibilityCorpus(t testing.TB, path string) compatibilityCorpus {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus compatibilityCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

// Analyze evaluates a statement in the corpus case's effective session time zone.
type Analyze func(zone, sql string) sqlanalyzer.Analysis

// Run checks classification, plan facts, and exact extent render/read-back.
func Run(t *testing.T, path string, analyze Analyze) {
	t.Helper()
	corpus := loadCompatibilityCorpus(t, path)
	if corpus.SchemaVersion != 1 || corpus.CorpusVersion == "" || corpus.MinimumInterval != corpusMinimumInterval {
		t.Fatalf("invalid corpus header: %+v", corpus)
	}
	if len(corpus.Omissions) == 0 || len(corpus.Cases) == 0 {
		t.Fatal("the corpus must include cases and record what it deliberately leaves out")
	}
	seen := make(map[string]struct{}, len(corpus.Cases))
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Name == "" || tc.ExpandedSQL == "" || tc.Rationale == "" || tc.SessionTimeZone == "" ||
				tc.QueryOrigin == "" || tc.Expected.CanonicalPolicy == "" {
				t.Fatalf("case lacks required documentation: %+v", tc)
			}
			if len(tc.MacroSource) > 0 && tc.GrafanaVersion == "" {
				t.Fatal("a Grafana macro case must record the Grafana version it was captured from")
			}
			if _, ok := seen[tc.Name]; ok {
				t.Fatalf("duplicate case %q", tc.Name)
			}
			seen[tc.Name] = struct{}{}
			wantMode, ok := corpusModes[tc.Expected.CacheMode]
			if !ok {
				t.Fatalf("unknown cache mode %q", tc.Expected.CacheMode)
			}
			analysis := analyze(tc.SessionTimeZone, tc.ExpandedSQL)
			if analysis.Mode != wantMode || string(analysis.Reason) != tc.Expected.AnalysisReason {
				t.Fatalf("got %s/%s (%v), want %s/%s", analysis.Mode, analysis.Reason, analysis.Err,
					tc.Expected.CacheMode, tc.Expected.AnalysisReason)
			}
			if wantMode != sqlanalyzer.CacheModeDelta {
				if analysis.Plan != nil || tc.Expected.ExtentRendering || tc.Expected.CanonicalPolicy != corpusPolicyNone {
					t.Fatalf("a case off the delta path must have no renderable plan: %+v", analysis.Plan)
				}
				return
			}
			assertCompatibilityPlan(t, tc, analysis.Plan, analyze)
		})
	}
}

func assertCompatibilityPlan(t *testing.T, tc compatibilityCase, plan *sqlanalyzer.QueryPlan, analyze Analyze) {
	t.Helper()
	want := tc.Expected
	if plan == nil || want.CanonicalPolicy != corpusPolicyRange || !want.ExtentRendering {
		t.Fatalf("a delta case needs a plan, a range-independent identity and extent rendering: %+v", want)
	}
	step, err := time.ParseDuration(want.Cadence)
	if err != nil {
		t.Fatal(err)
	}
	phase, err := time.ParseDuration(want.Phase)
	if err != nil {
		t.Fatal(err)
	}
	lower, err := time.Parse(time.RFC3339Nano, want.LowerBound)
	if err != nil {
		t.Fatal(err)
	}
	inputUnit, inputOK := corpusUnits[want.InputUnit]
	outputUnit, outputOK := corpusUnits[want.OutputUnit]
	if !inputOK || !outputOK {
		t.Fatalf("unknown unit in %q / %q", want.InputUnit, want.OutputUnit)
	}
	if plan.Step != step || plan.Phase != phase || plan.InputUnit != inputUnit || plan.OutputUnit != outputUnit ||
		plan.OutputColumn != want.OutputColumn || !slices.Equal(plan.GroupColumns, want.GroupColumns) {
		t.Fatalf("plan facts: step %v phase %v in %v out %v column %q groups %v", plan.Step, plan.Phase,
			plan.InputUnit, plan.OutputUnit, plan.OutputColumn, plan.GroupColumns)
	}
	if plan.LowerBound == nil || !plan.LowerBound.Value.Equal(lower) || plan.LowerBound.Inclusive != want.LowerInclusive {
		t.Fatalf("lower bound %+v, want %s", plan.LowerBound, want.LowerBound)
	}
	end := lower.Add(step)
	if want.OpenEnded {
		if plan.UpperBound != nil {
			t.Fatalf("expected an open-ended plan, got upper bound %+v", plan.UpperBound)
		}
	} else {
		upper, err := time.Parse(time.RFC3339Nano, want.UpperBound)
		if err != nil {
			t.Fatal(err)
		}
		if plan.UpperBound == nil || !plan.UpperBound.Value.Equal(upper) || plan.UpperBound.Inclusive != want.UpperInclusive {
			t.Fatalf("upper bound %+v, want %s", plan.UpperBound, want.UpperBound)
		}
		// every bound lies on the bucket grid, or partial buckets would be cached as whole ones
		if !sqlanalyzer.AlignedToBucket(upper, step, phase) {
			t.Fatalf("upper bound %s is off the grid", upper)
		}
	}
	if !sqlanalyzer.AlignedToBucket(lower, step, phase) {
		t.Fatalf("lower bound %s is off the grid", lower)
	}
	for _, fragment := range want.CanonicalContains {
		if !strings.Contains(plan.CanonicalSQL, fragment) {
			t.Errorf("canonical SQL lacks %q: %s", fragment, plan.CanonicalSQL)
		}
	}
	for _, fragment := range want.CanonicalExcludes {
		if strings.Contains(plan.CanonicalSQL, fragment) {
			t.Errorf("canonical SQL kept %q: %s", fragment, plan.CanonicalSQL)
		}
	}
	rendered, err := plan.RenderExtent(timeseries.Extent{Start: lower, End: end})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, corpusPlaceholder) || strings.Contains(rendered, "<$") {
		t.Fatalf("rendered SQL kept a placeholder: %s", rendered)
	}
	// what Trickster sends must mean the same statement: same identity, and exactly the extent asked for
	again := analyze(tc.SessionTimeZone, rendered)
	if again.Mode != sqlanalyzer.CacheModeDelta || again.Plan == nil || again.Plan.CanonicalSQL != plan.CanonicalSQL {
		t.Fatalf("rendering changed the statement:\n%s\n%v / %v", rendered, again.Mode, again.Err)
	}
	if again.Plan.LowerBound == nil || !again.Plan.LowerBound.Value.Equal(lower) || again.Plan.UpperBound == nil ||
		!again.Plan.UpperBound.Value.Equal(end.Add(step)) {
		t.Fatalf("rendered extent reads back as %v..%v, want %v..%v\n%s", again.Plan.LowerBound,
			again.Plan.UpperBound, lower, end.Add(step), rendered)
	}
}

// CheckGrafanaMacros requires an explicit case for every bundled PostgreSQL macro.
func CheckGrafanaMacros(t *testing.T, path string) {
	t.Helper()
	corpus := loadCompatibilityCorpus(t, path)
	var sources strings.Builder
	for _, tc := range corpus.Cases {
		sources.WriteString(strings.Join(tc.MacroSource, "\n"))
		sources.WriteByte('\n')
	}
	for _, macro := range []string{
		"$__time(", "$__timeEpoch(", "$__timeFilter(", "$__timeFrom(", "$__timeTo(", "$__timeGroup(",
		"$__timeGroupAlias(", "$__unixEpochFilter(", "$__unixEpochNanoFilter(", "$__unixEpochFrom(",
		"$__unixEpochTo(", "$__unixEpochGroup(", "$__unixEpochGroupAlias(", "$__interval",
	} {
		if !strings.Contains(sources.String(), macro) {
			t.Errorf("the corpus does not cover %s", macro)
		}
	}
}

// Benchmark measures analysis and immutable concurrent rendering per corpus case.
func Benchmark(b *testing.B, path string, analyze Analyze) {
	b.Helper()
	corpus := loadCompatibilityCorpus(b, path)
	for _, tc := range corpus.Cases {
		b.Run("Analyze/"+tc.Expected.CacheMode+"/"+tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = analyze(tc.SessionTimeZone, tc.ExpandedSQL)
			}
		})
		plan := analyze(tc.SessionTimeZone, tc.ExpandedSQL).Plan
		if plan == nil {
			continue
		}
		extent := timeseries.Extent{Start: plan.LowerBound.Value, End: plan.LowerBound.Value.Add(plan.Step)}
		b.Run("Render/"+tc.Name, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := plan.RenderExtent(extent); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
