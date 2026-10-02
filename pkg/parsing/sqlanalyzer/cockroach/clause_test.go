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

package cockroach

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

const (
	clauseTestTimeColumn = "ts"
	clauseOrderBy        = "ORDER BY"
	clauseLimit          = "LIMIT"
)

var (
	errClauseTestMalformed = errors.New("malformed sampling clause")
	errClauseTestRestore   = errors.New("restore failed")
	clauseTestNow          = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
)

type sampleByRewriter struct {
	failRestore bool
}

func (sampleByRewriter) Lift(statement string) (string, *LiftedClause, error) {
	start, end, ok := FindKeywords(statement, "SAMPLE", "BY")
	if !ok {
		return statement, nil, nil
	}
	scanner := sqlscan.New(statement[end:], sqlscan.Options{})
	token, ok := scanner.Next()
	if !ok || token.Kind != sqlscan.Number {
		return "", nil, errClauseTestMalformed
	}
	step, err := time.ParseDuration(scanner.Text(token))
	if err != nil {
		return "", nil, errClauseTestMalformed
	}
	end += token.End
	return statement[:start] + statement[end:], &LiftedClause{
		Payload: statement[start:end], ImplicitGrouping: true,
		Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: step},
	}, nil
}

func (r sampleByRewriter) Restore(rendered string, lifted *LiftedClause) (string, error) {
	if r.failRestore {
		return "", errClauseTestRestore
	}
	return InsertClause(rendered, lifted.Payload.(string), clauseOrderBy, clauseLimit), nil
}

type alignRewriter struct{}

func (alignRewriter) Lift(statement string) (string, *LiftedClause, error) {
	start, end, ok := FindKeywords(statement, "ALIGN")
	if !ok {
		return statement, nil, nil
	}
	scanner := sqlscan.New(statement[end:], sqlscan.Options{})
	token, ok := scanner.Next()
	if !ok || token.Kind != sqlscan.String {
		return "", nil, errClauseTestMalformed
	}
	step, err := time.ParseDuration(strings.Trim(scanner.Text(token), "'"))
	if err != nil {
		return "", nil, errClauseTestMalformed
	}
	end += token.End
	return statement[:start] + statement[end:], &LiftedClause{Payload: step}, nil
}

func (alignRewriter) Restore(rendered string, lifted *LiftedClause) (string, error) {
	return InsertClause(rendered, "ALIGN '"+lifted.Payload.(time.Duration).String()+"'", clauseOrderBy), nil
}

func alignedBucketMatcher(name string, args []tree.Expr, clauses []*LiftedClause) (BucketMatch, bool) {
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

func TestClauseRewriterDefinesTheBucket(t *testing.T) {
	analyzer := NewAnalyzer(Options{ClauseRewriters: []ClauseRewriter{sampleByRewriter{}}})
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

func TestClauseRewriterFailsClosed(t *testing.T) {
	analyzer := NewAnalyzer(Options{ClauseRewriters: []ClauseRewriter{sampleByRewriter{}}})
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
	} {
		analysis := analyzer.Analyze(test.statement, clauseTestNow)
		if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != test.reason {
			t.Fatalf("%s: got %v / %v / %v", name, analysis.Mode, analysis.Reason, analysis.Err)
		}
	}
	failing := NewAnalyzer(Options{ClauseRewriters: []ClauseRewriter{sampleByRewriter{failRestore: true}}})
	analysis := failing.Analyze("SELECT ts, avg(v) FROM cpu "+rangePredicate+"SAMPLE BY 5m", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != sqlanalyzer.ReasonUnsupportedFormat ||
		!errors.Is(analysis.Err, errClauseTestRestore) {
		t.Fatalf("a failed restore must fail closed: %v / %v / %v", analysis.Mode, analysis.Reason, analysis.Err)
	}
	// an unparsable statement keeps its object eligibility by its own leading keyword
	analysis = analyzer.Analyze("SELECT ts FROM FROM cpu SAMPLE BY 5m", clauseTestNow)
	if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != sqlanalyzer.ReasonInvalidSQL {
		t.Fatalf("got %v / %v", analysis.Mode, analysis.Reason)
	}
}

func TestClauseBucketMatcherReadsThePayload(t *testing.T) {
	analyzer := NewAnalyzer(Options{
		// both rewriters are configured; only the one whose clause is present lifts
		ClauseRewriters:      []ClauseRewriter{sampleByRewriter{}, alignRewriter{}},
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
	if err != nil || !strings.Contains(rendered, " ALIGN '10m0s' ORDER BY") {
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
	first, second := &LiftedClause{Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: time.Minute}},
		&LiftedClause{Bucket: &BucketMatch{TimeColumn: clauseTestTimeColumn, Step: time.Hour}}
	items := tree.SelectExprs{{Expr: tree.NewUnresolvedName(clauseTestTimeColumn)}}
	if _, _, err := clauseBucket(items, []*LiftedClause{first, second}); !errors.Is(err, ErrAmbiguousTimeAxis) {
		t.Fatalf("two bucket-defining clauses must be ambiguous, got %v", err)
	}
	if _, _, err := clauseBucket(items, []*LiftedClause{{Payload: 1}}); !errors.Is(err, ErrUnsupportedBucket) {
		t.Fatalf("a clause with no bucket defines none, got %v", err)
	}
}

func TestFindKeywordsAndInsertClause(t *testing.T) {
	const rendered = "SELECT (SELECT 1 ORDER BY 1), 'ORDER BY' FROM t WHERE ts >= <$TS1$> AND ts < <$TS2$> ORDER BY ts LIMIT 5"
	start, end, ok := FindKeywords(rendered, "order", "by")
	if !ok || rendered[start:end] != clauseOrderBy || start != strings.LastIndex(rendered, clauseOrderBy) {
		t.Fatalf("expected the top-level ORDER BY, got %d..%d %t", start, end, ok)
	}
	if _, _, ok = FindKeywords(rendered, "GROUP", "BY"); ok {
		t.Fatal("found a phrase that is absent")
	}
	if _, _, ok = FindKeywords(rendered); ok {
		t.Fatal("no keywords can match nothing")
	}
	if start, _, ok = FindKeywords("a ORDER ORDER BY b", "ORDER", "BY"); !ok || start != 8 {
		t.Fatalf("a restarted run must anchor at its second start, got %d %t", start, ok)
	}
	got := InsertClause(rendered, "SAMPLE BY 1h", clauseLimit, clauseOrderBy)
	if !strings.Contains(got, "<$TS2$> SAMPLE BY 1h ORDER BY ts LIMIT 5") {
		t.Fatalf("expected the clause before the earliest anchor: %q", got)
	}
	if got = InsertClause("SELECT 1 ", "SAMPLE BY 1h", clauseOrderBy); got != "SELECT 1 SAMPLE BY 1h" {
		t.Fatalf("expected the clause at the end: %q", got)
	}
}
