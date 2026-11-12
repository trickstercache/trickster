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

package pgparser

import (
	"errors"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
)

var (
	sampleDialect = NewDialect(DialectSpec{Clauses: []string{"SAMPLE  by"}, DurationLiterals: true})
	alignDialect  = NewDialect(DialectSpec{
		Clauses: []string{"ALIGN", " "}, FunctionSuffixes: []string{"RANGE"}, SuffixOptions: []string{"fill"},
	})
	backtickDialect = NewDialect(DialectSpec{BacktickIdentifiers: true})
)

func parseClause(t *testing.T, sql string, d *Dialect) *ast.SelectClause {
	t.Helper()
	stmt, err := ParseQuery(sql, Options{Dialect: d})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	clause, ok := stmt.Select.(*ast.SelectClause)
	if !ok {
		t.Fatalf("%s: got %T", sql, stmt.Select)
	}
	return clause
}

func TestDialectClauseParsesAsWritten(t *testing.T) {
	const sql = "SELECT ts, avg(v) FROM t WHERE ts >= '2024-01-01' SAMPLE By 1h FILL(NULL) ALIGN TO CALENDAR ORDER BY ts"
	clause := parseClause(t, sql, sampleDialect)
	if len(clause.Dialect) != 1 {
		t.Fatalf("got %d dialect clauses", len(clause.Dialect))
	}
	got := clause.Dialect[0]
	if got.Keyword != "sample by" || got.Body != "1h FILL(NULL) ALIGN TO CALENDAR" ||
		got.Text != "SAMPLE By 1h FILL(NULL) ALIGN TO CALENDAR" {
		t.Errorf("got %+v", *got)
	}
	if printed := printQuery(sql, Options{Dialect: sampleDialect}); printed != sql {
		t.Errorf("printed %q", printed)
	}
	if _, err := ParseQuery(sql, Options{}); err == nil {
		t.Error("expected the base grammar to reject SAMPLE BY")
	}
}

func TestDialectClauseBoundaries(t *testing.T) {
	inner := "SELECT * FROM (SELECT ts FROM t SAMPLE BY 1h) AS s"
	stmt, err := ParseQuery(inner, Options{Dialect: sampleDialect})
	if err != nil {
		t.Fatal(err)
	}
	outer := stmt.Select.(*ast.SelectClause)
	if len(outer.Dialect) != 0 || ast.AsString(stmt) != inner {
		t.Errorf("got %d outer clauses, printed %q", len(outer.Dialect), ast.AsString(stmt))
	}
	twice := parseClause(t, "SELECT ts FROM t SAMPLE BY 1h SAMPLE BY 2h", sampleDialect)
	if len(twice.Dialect) != 2 || twice.Dialect[1].Body != "2h" {
		t.Errorf("got %d clauses", len(twice.Dialect))
	}
	keywordOnly := parseClause(t, "SELECT ts FROM t SAMPLE BY", sampleDialect)
	if len(keywordOnly.Dialect) != 1 || keywordOnly.Dialect[0].Body != "" {
		t.Errorf("got %+v", keywordOnly.Dialect)
	}
	for _, sql := range []string{
		"SELECT ts FROM t SAMPLE BY 'unterminated",
		"SELECT ts FROM t SAMPLE BY 1h WHERE v > 1",
		"SELECT ts FROM t SAMPLE BY 1h2",
	} {
		if _, err := ParseQuery(sql, Options{Dialect: sampleDialect}); err == nil {
			t.Errorf("%s: expected an error", sql)
		}
	}
}

func TestDialectFallsBackToTheBaseGrammar(t *testing.T) {
	for _, sql := range []string{
		"SELECT align FROM t",
		"SELECT -1, align FROM t WHERE align > 2 ORDER BY align",
		"SELECT count(align) FROM t GROUP BY host, align",
	} {
		if printed := printQuery(sql, Options{Dialect: alignDialect}); printed != sql {
			t.Errorf("%s: printed %q", sql, printed)
		}
	}
	if _, err := ParseQuery("SELECT align FROM t; SELECT 1", Options{Dialect: alignDialect}); !errors.Is(err, ErrMultipleStatements) {
		t.Errorf("got %v", err)
	}
	var syntax *SyntaxError
	if _, err := ParseQuery("SELECT ts FROM t ALIGN '5s' WHERE", Options{Dialect: alignDialect}); !errors.As(err, &syntax) {
		t.Errorf("got %v", err)
	}
}

func TestFunctionSuffix(t *testing.T) {
	const sql = "SELECT ts, host, min(cpu) RANGE '10s' FILL LINEAR, max(cpu) RANGE '1m' FILL -1 " +
		"FROM host_cpu ALIGN '5s' BY (host) FILL PREV ORDER BY ts"
	clause := parseClause(t, sql, alignDialect)
	first := clause.Exprs[2].Expr.(*ast.FuncExpr)
	if first.Suffix == nil || first.Suffix.Keyword != "range" || first.Suffix.Body != "'10s' FILL LINEAR" {
		t.Fatalf("got %+v", first.Suffix)
	}
	if second := clause.Exprs[3].Expr.(*ast.FuncExpr); second.Suffix == nil || second.Suffix.Text != "RANGE '1m' FILL -1" {
		t.Fatalf("got %+v", second.Suffix)
	}
	if len(clause.Dialect) != 1 || clause.Dialect[0].Body != "'5s' BY (host) FILL PREV" {
		t.Fatalf("got %+v", clause.Dialect)
	}
	if printed := printQuery(sql, Options{Dialect: alignDialect}); printed != sql {
		t.Errorf("printed %q", printed)
	}
	for _, sql := range []string{
		"SELECT sum(v) OVER (ORDER BY f(ts) RANGE '1 day' PRECEDING) FROM t",
		"SELECT sum(v) OVER (ORDER BY f(ts) RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t",
	} {
		stmt, err := ParseQuery(sql, Options{Dialect: alignDialect})
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if fn := stmt.Select.(*ast.SelectClause).Exprs[0].Expr.(*ast.FuncExpr); fn.Suffix != nil {
			t.Errorf("%s: a window frame read as a suffix", sql)
		}
	}
	for _, sql := range []string{
		"SELECT min(cpu) RANGE '10s' FILL FROM t", "SELECT min(cpu) RANGE '10s' FILL", "SELECT min(cpu) RANGE '10s' FILL - FROM t",
	} {
		if _, err := ParseQuery(sql, Options{Dialect: alignDialect}); err == nil {
			t.Errorf("%s: expected an error", sql)
		}
	}
}

func TestDurationLiterals(t *testing.T) {
	const sql = "SELECT ts FROM t WHERE ts > now() - 1h AND ts < now() - 30M"
	const want = "SELECT ts FROM t WHERE (ts > (now() - 1h)) AND (ts < (now() - 30M))"
	if printed := printQuery(sql, Options{Dialect: sampleDialect}); printed != want {
		t.Errorf("printed %q", printed)
	}
	clause := parseClause(t, sql, sampleDialect)
	if !strings.Contains(clause.Where.Expr.String(), "1h") {
		t.Errorf("got %s", clause.Where.Expr)
	}
	var duration *ast.DurationVal
	ast.WalkExpr(visitFunc(func(expr ast.Expr) {
		if d, ok := expr.(*ast.DurationVal); ok && duration == nil {
			duration = d
		}
	}), clause.Where.Expr)
	if duration == nil || duration.Text != "1h" || duration.String() != "1h" {
		t.Errorf("got %v", duration)
	}
	for _, sql := range []string{"SELECT 1h_", "SELECT 1.5h", "SELECT 0x1h"} {
		if _, err := ParseQuery(sql, Options{Dialect: sampleDialect}); err == nil {
			t.Errorf("%s: expected an error", sql)
		}
	}
	if _, err := ParseQuery("SELECT 1h", Options{}); err == nil {
		t.Error("expected the base grammar to reject a duration")
	}
}

type visitFunc func(ast.Expr)

func (f visitFunc) VisitPre(expr ast.Expr) (bool, ast.Expr) {
	f(expr)
	return true, expr
}

func (visitFunc) VisitPost(expr ast.Expr) ast.Expr { return expr }

func TestBacktickIdentifiers(t *testing.T) {
	const sql = "SELECT `my col`, `a``b`, \"q\" FROM `metrics`"
	stmt, err := ParseQuery(sql, Options{Dialect: backtickDialect})
	if err != nil {
		t.Fatal(err)
	}
	const want = "SELECT `my col`, `a``b`, q FROM metrics"
	if got := ast.AsStringWithFlags(stmt, ast.FmtBacktickIdentifiers); got != want {
		t.Errorf("got %q", got)
	}
	if got := ast.AsString(stmt); got != `SELECT "my col", "a`+"`"+`b", q FROM metrics` {
		t.Errorf("got %q", got)
	}
	if _, err := ParseQuery(sql, Options{}); err == nil {
		t.Error("expected the base grammar to reject backticks")
	}
	if _, err := ParseQuery("SELECT `open", Options{Dialect: backtickDialect}); err == nil {
		t.Error("expected an unterminated identifier to fail")
	}
}

func TestDialectClausePhraseOpensAParenthesis(t *testing.T) {
	d := NewDialect(DialectSpec{Clauses: []string{"PARTITION BY", "INTERVAL (", "FILL ("}, DurationLiterals: true})
	const sql = "SELECT _wstart, avg(v) FROM m WHERE ts >= '2024-01-01' PARTITION BY tbname INTERVAL(10s, 2s) FILL(NULL) ORDER BY _wstart"
	clause := parseClause(t, sql, d)
	if len(clause.Dialect) != 3 || clause.Dialect[1].Keyword != "interval (" || clause.Dialect[1].Body != "10s, 2s)" ||
		clause.Dialect[2].Text != "FILL(NULL)" {
		t.Fatalf("got %+v", clause.Dialect)
	}
	if printed := printQuery(sql, Options{Dialect: d}); printed != sql {
		t.Errorf("printed %q", printed)
	}
	// INTERVAL as a PostgreSQL type keeps its meaning where no clause can parse
	if printed := printQuery("SELECT INTERVAL '1 day'", Options{Dialect: d}); printed != "SELECT INTERVAL '1 day'" {
		t.Errorf("printed %q", printed)
	}
}

func TestDialectWithoutRewrites(t *testing.T) {
	if (*Dialect)(nil).rewrites() || backtickDialect.rewrites() || !sampleDialect.rewrites() {
		t.Error("unexpected rewrite state")
	}
	if len(alignDialect.clauses) != 1 {
		t.Errorf("a blank clause phrase was kept: %+v", alignDialect.clauses)
	}
}
