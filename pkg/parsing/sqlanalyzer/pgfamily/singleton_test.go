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

const (
	singletonSelect = `SELECT date_bin(INTERVAL '1 minute', ts) AS b, host, avg(v) AS v FROM cpu ` +
		`WHERE ts >= '2024-01-01T00:00:00Z' AND ts < '2024-01-01T01:00:00Z'`
	singletonGroup = " GROUP BY b, host"
	trailingComma  = ",)"
)

func TestSingletonTupleRendersWithoutTrailingComma(t *testing.T) {
	a := newDataFusionAnalyzer()
	now := time.Unix(1704153600, 0)
	extent := timeseries.Extent{Start: time.Unix(1704067200, 0), End: time.Unix(1704070740, 0)}
	tests := []struct {
		name, predicate, want string
	}{
		{"string IN", " AND host IN ('abc')", "host IN ('abc')"},
		{"number NOT IN", " AND v NOT IN (1)", "v NOT IN (1)"},
		{"two values unchanged", " AND host IN ('a', 'b')", "host IN ('a', 'b')"},
		{"ROW comparison", " AND ROW(host) = ROW('a')", "ROW(host) = ROW('a')"},
		{"nested list", " AND host IN (lower(upper('a')))", "host IN (lower(upper('a')))"},
		{"literal with a comma and paren", " AND host IN ('a,)')", "host IN ('a,)')"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := a.Analyze(singletonSelect+test.predicate+singletonGroup, now)
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
				t.Fatalf("mode = %v, reason = %s, err = %v", analysis.Mode, analysis.Reason, analysis.Err)
			}
			rendered, err := analysis.Plan.RenderExtent(extent)
			if err != nil {
				t.Fatal(err)
			}
			for _, sql := range []string{rendered, analysis.Plan.CanonicalSQL} {
				if !strings.Contains(sql, test.want) {
					t.Errorf("%q does not contain %q", sql, test.want)
				}
				if strings.Contains(strings.ReplaceAll(sql, "'a,)'", ""), trailingComma) {
					t.Errorf("%q has a one-element tuple's trailing comma", sql)
				}
			}
			// the rendered statement reads back to the same plan
			again := a.Analyze(rendered, now)
			if again.Plan == nil || again.Plan.CanonicalSQL != analysis.Plan.CanonicalSQL {
				t.Errorf("re-analysis of %q changed the plan: %+v", rendered, again)
			}
		})
	}
}

func TestSingletonTupleKeepsVolatileCheck(t *testing.T) {
	statement := `SELECT date_bin(INTERVAL '1 minute', ts) AS b, bool_or(v IN (random())) AS r FROM cpu ` +
		`WHERE ts >= '2024-01-01T00:00:00Z' GROUP BY b`
	analysis := newDataFusionAnalyzer().Analyze(statement, time.Unix(1704153600, 0))
	if analysis.Reason != sqlanalyzer.ReasonNondeterministic {
		t.Fatalf("reason = %s, want %s", analysis.Reason, sqlanalyzer.ReasonNondeterministic)
	}
}

func TestParserPanicIsAnError(t *testing.T) {
	parse := parseQuery
	t.Cleanup(func() { parseQuery = parse })
	parseQuery = func(string, pgparser.Options) (*ast.Select, error) { panic("parser bug") }
	if _, err := ParseOne(singletonSelect); !errors.Is(err, ErrParserFailure) {
		t.Fatalf("ParseOne error = %v, want %v", err, ErrParserFailure)
	}
	analysis := newDataFusionAnalyzer().Analyze(singletonSelect, time.Unix(1704153600, 0))
	if analysis.Mode != sqlanalyzer.CacheModeObject || !errors.Is(analysis.Err, ErrParserFailure) {
		t.Fatalf("mode = %v, err = %v", analysis.Mode, analysis.Err)
	}
}

func TestNonQueryStatementsSkipTheParser(t *testing.T) {
	a := newDataFusionAnalyzer()
	for _, statement := range []string{
		"DO $$ BEGIN END $$", "INSERT INTO t VALUES (1)", "/* c */ SET x = 1", "garbage", "$1", "-- only a comment",
	} {
		analysis := a.Analyze(statement, time.Unix(1704153600, 0))
		if analysis.Mode != sqlanalyzer.CacheModeNone || analysis.Reason != sqlanalyzer.ReasonUnsupportedStatement ||
			!errors.Is(analysis.Err, ErrUnsupportedStatement) {
			t.Errorf("%q: mode %v, reason %s, err %v", statement, analysis.Mode, analysis.Reason, analysis.Err)
		}
	}
	if _, err := ParseOne("-- only a comment"); !errors.Is(err, ErrNoStatement) {
		t.Errorf("a lone comment: err = %v, want %v", err, ErrNoStatement)
	}
	for _, statement := range []string{"(SELECT 1)", "/* c */ WITH x AS (SELECT 1) SELECT * FROM x", "VALUES (1)", "TABLE t"} {
		if !leadingKeywordIsQuery(statement) {
			t.Errorf("%q was not read as a query", statement)
		}
	}
}
