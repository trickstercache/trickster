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

// Package sqlgolden pins what a SQL analyzer or parser makes of a shared statement corpus, so a
// parser change shows up as a golden diff. Run a package's tests with -update to rewrite its goldens.
package sqlgolden

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

//go:embed testdata/statements.json
var statementsJSON []byte

const (
	goldenDir     = "testdata/sqlgolden"
	goldenExt     = ".jsonl"
	renderBuckets = 3
)

// Now is the fixed analysis time every golden uses
var Now = time.Date(2026, 1, 15, 12, 0, 7, 0, time.UTC)

// Statements returns the shared corpus
func Statements(t testing.TB) []string {
	t.Helper()
	var statements []string
	if err := json.Unmarshal(statementsJSON, &statements); err != nil {
		t.Fatal(err)
	}
	return statements
}

// Analyze evaluates one corpus statement at the fixed time
type Analyze func(sql string, now time.Time) sqlanalyzer.Analysis

type bound struct {
	Value     string `json:"value"`
	Inclusive bool   `json:"inclusive,omitempty"`
}

type planRecord struct {
	CanonicalSQL        string                 `json:"canonical_sql"`
	TimeColumn          string                 `json:"time_column"`
	OutputColumn        string                 `json:"output_column"`
	Step                string                 `json:"step"`
	Phase               string                 `json:"phase"`
	OutputUnit          int                    `json:"output_unit"`
	InputUnit           int                    `json:"input_unit"`
	Lower               *bound                 `json:"lower,omitempty"`
	Upper               *bound                 `json:"upper,omitempty"`
	RawLower            *bound                 `json:"raw_lower,omitempty"`
	RawUpper            *bound                 `json:"raw_upper,omitempty"`
	UpperIsNow          bool                   `json:"upper_is_now,omitempty"`
	GroupColumns        []string               `json:"group_columns,omitempty"`
	BucketGroupIndex    int                    `json:"bucket_group_index"`
	DropsPartialBuckets bool                   `json:"drops_partial_buckets,omitempty"`
	ValueColumns        []string               `json:"value_columns,omitempty"`
	Ordering            []timeseries.OrderTerm `json:"ordering,omitempty"`
	OutputFormat        byte                   `json:"output_format,omitempty"`
	ReadsZone           bool                   `json:"reads_zone,omitempty"`
	ZonedBounds         bool                   `json:"zoned_bounds,omitempty"`
	Directives          timeseries.Directives  `json:"directives"`
	Extent              string                 `json:"extent"`
	Range               string                 `json:"range"`
	OpenRange           string                 `json:"open_range"`
}

type analysisRecord struct {
	SQL    string      `json:"sql"`
	Mode   string      `json:"mode"`
	Reason string      `json:"reason"`
	Failed bool        `json:"failed,omitempty"`
	Plan   *planRecord `json:"plan,omitempty"`
}

type textRecord struct {
	SQL string `json:"sql"`
	Out string `json:"out"`
}

func boundOf(b *sqlanalyzer.Bound) *bound {
	if b == nil {
		return nil
	}
	return &bound{Value: b.Value.UTC().Format(time.RFC3339Nano), Inclusive: b.Inclusive}
}

func rendered(sql string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return sql
}

func planOf(p *sqlanalyzer.QueryPlan) *planRecord {
	r := &planRecord{
		CanonicalSQL: p.CanonicalSQL, TimeColumn: p.TimeColumn, OutputColumn: p.OutputColumn,
		Step: p.Step.String(), Phase: p.Phase.String(), OutputUnit: int(p.OutputUnit), InputUnit: int(p.InputUnit),
		Lower: boundOf(p.LowerBound), Upper: boundOf(p.UpperBound), RawLower: boundOf(p.RawLower),
		RawUpper: boundOf(p.RawUpper), UpperIsNow: p.UpperIsNow, GroupColumns: p.GroupColumns,
		BucketGroupIndex: p.BucketGroupIndex, DropsPartialBuckets: p.DropsPartialBuckets,
		ValueColumns: p.ValueColumns, Ordering: p.Ordering, OutputFormat: p.OutputFormat,
		ReadsZone: p.ReadsZone, ZonedBounds: p.ZonedBounds, Directives: p.Directives,
	}
	if p.LowerBound == nil || p.Step <= 0 {
		return r
	}
	// a fixed window on the plan's own grid, so every render is reproducible
	start := p.LowerBound.Value
	r.Extent = rendered(p.RenderExtent(timeseries.Extent{Start: start, End: start.Add((renderBuckets - 1) * p.Step)}))
	r.Range = rendered(p.RenderRange(timeseries.PartialBucket{Lower: start, Upper: start.Add(p.Step / 2)}))
	r.OpenRange = rendered(p.RenderRange(timeseries.PartialBucket{Lower: start.Add(renderBuckets * p.Step)}))
	return r
}

// RunAnalyzer compares an analyzer's reading of the corpus with testdata/sqlgolden/<name>.jsonl
func RunAnalyzer(t *testing.T, name string, analyze Analyze) {
	t.Helper()
	statements := Statements(t)
	records := make([]any, len(statements))
	for i, sql := range statements {
		a := analyze(sql, Now)
		r := &analysisRecord{SQL: sql, Mode: a.Mode.String(), Reason: string(a.Reason), Failed: a.Err != nil}
		if a.Plan != nil {
			r.Plan = planOf(a.Plan)
		}
		records[i] = r
	}
	compare(t, name, records)
}

// RunText compares a statement-to-text function, such as parse-and-print, with its golden
func RunText(t *testing.T, name string, render func(sql string) string) {
	t.Helper()
	statements := Statements(t)
	records := make([]any, len(statements))
	for i, sql := range statements {
		records[i] = &textRecord{SQL: sql, Out: render(sql)}
	}
	compare(t, name, records)
}

func compare(t *testing.T, name string, records []any) {
	t.Helper()
	lines := make([][]byte, len(records))
	for i, r := range records {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
		lines[i] = buf.Bytes()
	}
	path := filepath.Join(goldenDir, name+goldenExt)
	if *golden.Update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Join(lines, nil), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	wantLines := bytes.SplitAfter(want, []byte{'\n'})
	if n := len(wantLines); n > 0 && len(wantLines[n-1]) == 0 {
		wantLines = wantLines[:n-1]
	}
	if len(wantLines) != len(lines) {
		t.Fatalf("%s holds %d records, the corpus has %d (run with -update)", path, len(wantLines), len(lines))
	}
	const maxReported = 10
	differences := 0
	for i := range lines {
		if bytes.Equal(lines[i], wantLines[i]) {
			continue
		}
		if differences++; differences <= maxReported {
			t.Errorf("%s record %d differs\n got: %s\nwant: %s", path, i+1, lines[i], wantLines[i])
		}
	}
	if differences > maxReported {
		t.Errorf("%s: %d records differ in all", path, differences)
	}
}
