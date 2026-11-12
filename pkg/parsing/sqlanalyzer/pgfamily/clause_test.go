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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser"
	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const clauseTestTimeColumn = "ts"

var (
	errClauseTestMalformed = errors.New("malformed sampling clause")
	clauseTestNow          = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
)

const (
	clauseSampleBy = "sample by"
	clauseAlign    = "align"
)

var clauseTestSyntax = pgparser.DialectSpec{
	Clauses: []string{"SAMPLE BY", "ALIGN"}, FunctionSuffixes: []string{"RANGE"}, DurationLiterals: true,
}

func sampleByMatcher(clause *ast.DialectClause) (*Clause, error) {
	if clause.Keyword != clauseSampleBy {
		return nil, nil
	}
	step, err := time.ParseDuration(clause.Body)
	if err != nil {
		return nil, errClauseTestMalformed
	}
	return &Clause{
		ImplicitGrouping: true, Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: step},
	}, nil
}

func alignMatcher(clause *ast.DialectClause) (*Clause, error) {
	if clause.Keyword != clauseAlign {
		return nil, nil
	}
	step, err := time.ParseDuration(strings.Trim(clause.Body, "'"))
	if err != nil {
		return nil, errClauseTestMalformed
	}
	return &Clause{Payload: step}, nil
}

func alignedBucketMatcher(name string, args []ast.Expr, clauses []*Clause) (BucketMatch, bool) {
	if name != "bucket" || len(args) != 1 {
		return BucketMatch{}, false
	}
	column, ok := ColumnName(args[0])
	for _, clause := range clauses {
		if step, isStep := clause.Payload.(time.Duration); ok && isStep {
			return BucketMatch{TimeColumn: column, Step: step}, true
		}
	}
	return BucketMatch{}, false
}

func clauseTestExtent() timeseries.Extent {
	return timeseries.Extent{
		Start: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 9, 18, 10, 55, 0, 0, time.UTC),
	}
}

func sampleByAnalyzer() *Analyzer {
	return NewAnalyzer(Options{Syntax: clauseTestSyntax, ClauseMatchers: []ClauseMatcher{sampleByMatcher}})
}

func TestClauseMatcherDefinesTheBucket(t *testing.T) {
	analyzer := sampleByAnalyzer()
	analysis := analyzer.Analyze("SELECT ts AS time, host, avg(v) AS value FROM cpu "+
		"WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' "+
		"SAMPLE BY 5m ORDER BY time", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("expected a delta plan, got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
	plan := analysis.Plan
	if plan.Step != 5*time.Minute || plan.TimeColumn != clauseTestTimeColumn || plan.OutputColumn != "time" ||
		len(plan.GroupColumns) != 1 || plan.GroupColumns[0] != "host" {
		t.Fatalf("unexpected plan %+v", plan)
	}
	if !strings.Contains(plan.CanonicalSQL, " SAMPLE BY 5m ORDER BY") {
		t.Fatalf("the clause must be part of the statement's identity: %q", plan.CanonicalSQL)
	}
	rendered, err := plan.RenderExtent(clauseTestExtent())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "'2026-09-18T10:00:00Z'") || !strings.Contains(rendered, "'2026-09-18T11:00:00Z'") ||
		!strings.Contains(rendered, " SAMPLE BY 5m ORDER BY") || strings.Contains(rendered, "<$") {
		t.Fatalf("unexpected extent query %q", rendered)
	}
}

func TestClauseMatcherFailsClosed(t *testing.T) {
	analyzer := sampleByAnalyzer()
	const rangePredicate = "WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' "
	for name, test := range map[string]struct {
		statement string
		reason    sqlanalyzer.AnalysisReason
	}{
		"malformed clause":      {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY fortnight", sqlanalyzer.ReasonUnsupportedBucket},
		"bucket column missing": {"SELECT avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedBucket},
		"bucket column twice":   {"SELECT ts, ts AS again, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedBucket},
		"explicit group by":     {"SELECT ts, host, avg(v) FROM cpu " + rangePredicate + "GROUP BY host SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedGrouping},
		"qualified star":        {"SELECT ts, cpu.*, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedGrouping},
		"bare star":             {"SELECT ts, *, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedGrouping},
		"no clause, no bucket":  {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "GROUP BY ts", sqlanalyzer.ReasonUnsupportedBucket},
		"quoted clause ignored": {"SELECT ts, 'SAMPLE BY 5m' FROM cpu " + rangePredicate + "GROUP BY ts", sqlanalyzer.ReasonUnsupportedBucket},
		"unclaimed clause":      {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m ALIGN '1m'", sqlanalyzer.ReasonUnsupportedFormat},
		"function suffix":       {"SELECT ts, avg(v) RANGE '5m' FROM cpu " + rangePredicate + "SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedFormat},
		"suffix in order by":    {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m ORDER BY max(v) RANGE '5m'", sqlanalyzer.ReasonUnsupportedFormat},
		"suffix in group by":    {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "GROUP BY max(v) RANGE '5m'", sqlanalyzer.ReasonUnsupportedFormat},
		"suffix in where":       {"SELECT ts, avg(v) FROM cpu " + rangePredicate + "AND max(v) RANGE '5m' > 1 SAMPLE BY 5m", sqlanalyzer.ReasonUnsupportedFormat},
	} {
		analysis := analyzer.Analyze(test.statement, clauseTestNow)
		if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != test.reason {
			t.Fatalf("%s: got %v / %v / %v", name, analysis.Mode, analysis.Reason, analysis.Err)
		}
	}
	for _, test := range []string{
		"SELECT ts, avg(v) FROM cpu " + rangePredicate + "SAMPLE BY 5m ALIGN '1m'",
		"SELECT ts, avg(v) RANGE '5m' FROM cpu " + rangePredicate + "SAMPLE BY 5m",
	} {
		if analysis := analyzer.Analyze(test, clauseTestNow); !errors.Is(analysis.Err, ErrUnsupportedDialectSyntax) {
			t.Errorf("%s: got %v", test, analysis.Err)
		}
	}
	if analysis := analyzer.Analyze("SELECT ts, avg(v) FROM cpu "+rangePredicate+"SAMPLE BY 5m",
		clauseTestNow); analysis.Mode != sqlanalyzer.CacheModeDelta {
		t.Errorf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
	// an unparsable statement keeps its object eligibility by its own leading keyword
	analysis := analyzer.Analyze("SELECT ts FROM FROM cpu SAMPLE BY 5m", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != sqlanalyzer.ReasonInvalidSQL {
		t.Fatalf("got %v / %v", analysis.Mode, analysis.Reason)
	}
}

func TestClauseBucketMatcherReadsThePayload(t *testing.T) {
	analyzer := NewAnalyzer(Options{
		// both matchers are configured; only the one whose clause is present claims
		Syntax:               clauseTestSyntax,
		ClauseMatchers:       []ClauseMatcher{sampleByMatcher, alignMatcher},
		ClauseBucketMatchers: []ClauseBucketMatcher{alignedBucketMatcher},
		BucketMatchers:       []BucketMatcher{DateBinMatcher},
	})
	analysis := analyzer.Analyze("SELECT bucket(ts) AS time, max(v) FROM cpu "+
		"WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' "+
		"GROUP BY time ALIGN '10m' ORDER BY time", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan.Step != 10*time.Minute {
		t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
	rendered, err := analysis.Plan.RenderExtent(clauseTestExtent())
	if err != nil || !strings.Contains(rendered, " ALIGN '10m' ORDER BY") {
		t.Fatalf("unexpected extent query %q, %v", rendered, err)
	}
	// without its clause the function has no cadence, and a plain matcher still works
	if got := analyzer.Analyze("SELECT bucket(ts) AS time, max(v) FROM cpu WHERE ts >= '2026-09-18T08:00:00Z' "+
		"AND ts < '2026-09-18T11:00:00Z' GROUP BY time", clauseTestNow); got.Reason != sqlanalyzer.ReasonUnsupportedBucket {
		t.Fatalf("got %v / %v", got.Mode, got.Reason)
	}
	if got := analyzer.Analyze("SELECT date_bin(INTERVAL '5 minutes', ts) AS time, max(v) FROM cpu "+
		"WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' GROUP BY time", clauseTestNow); got.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("got %v / %v / %v", got.Mode, got.Reason, got.Err)
	}
}

func TestClauseBucketAmbiguity(t *testing.T) {
	first, second := &Clause{Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: time.Minute}},
		&Clause{Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: time.Hour}}
	items := ast.SelectExprs{{Expr: ast.NewUnresolvedName(clauseTestTimeColumn)}}
	if _, _, err := clauseBucket(items, []*Clause{first, second}); !errors.Is(err, ErrAmbiguousTimeAxis) {
		t.Fatalf("two bucket-defining clauses must be ambiguous, got %v", err)
	}
	if _, _, err := clauseBucket(items, []*Clause{{Payload: 1}}); !errors.Is(err, ErrUnsupportedBucket) {
		t.Fatalf("a clause with no bucket defines none, got %v", err)
	}
}

func TestBacktickIdentifiersRenderAsWritten(t *testing.T) {
	analyzer := NewAnalyzer(Options{
		Syntax: pgparser.DialectSpec{BacktickIdentifiers: true}, BucketMatchers: []BucketMatcher{DateBinMatcher},
	})
	analysis := analyzer.Analyze("SELECT date_bin(INTERVAL '5 minutes', `event time`) AS time, max(v) FROM `cpu load` "+
		"WHERE `event time` >= '2026-09-18T08:00:00Z' AND `event time` < '2026-09-18T11:00:00Z' GROUP BY time", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeDelta {
		t.Fatalf("got %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
	rendered, err := analysis.Plan.RenderExtent(clauseTestExtent())
	if err != nil || !strings.Contains(rendered, "FROM `cpu load` WHERE (`event time` >= ") || strings.Contains(rendered, `"`) {
		t.Fatalf("unexpected extent query %q, %v", rendered, err)
	}
	if analysis.Plan.TimeColumn != "event time" {
		t.Errorf("got time column %q", analysis.Plan.TimeColumn)
	}
}
