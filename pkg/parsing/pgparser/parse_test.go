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

package pgparser

import (
	"errors"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlgolden"
)

const (
	parseFailed  = "parse error"
	selectOne    = "SELECT 1"
	castIntQuery = "SELECT a::int, CAST(b AS integer) FROM t"
	selectPrefix = "SELECT "
	errorPrefix  = "ERR "
)

func printQuery(sql string, opts Options) string {
	stmt, err := ParseQuery(sql, opts)
	if err != nil || stmt == nil {
		return parseFailed
	}
	return ast.AsString(stmt)
}

func TestGoldenParse(t *testing.T) {
	sqlgolden.RunText(t, "parse", func(sql string) string { return printQuery(sql, Options{}) })
}

func TestGoldenParseInt4(t *testing.T) {
	sqlgolden.RunText(t, "parse_int4", func(sql string) string {
		return printQuery(sql, Options{NakedIntIsInt4: true})
	})
}

// every statement the parser accepts prints as SQL that reads back to the same text
func TestPrintIsAFixedPoint(t *testing.T) {
	for _, sql := range sqlgolden.Statements(t) {
		first := printQuery(sql, Options{})
		if first == parseFailed {
			continue
		}
		if again := printQuery(first, Options{}); again != first {
			t.Errorf("%q printed %q, which reads back as %q", sql, first, again)
		}
	}
}

func TestNakedInt(t *testing.T) {
	for opts, width := range map[Options]int32{{}: 64, {NakedIntIsInt4: true}: 32} {
		stmt, err := ParseQuery(castIntQuery, opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := ast.AsString(stmt); got != castIntQuery {
			t.Errorf("printed %q, want it as written", got)
		}
		for _, item := range stmt.Select.(*ast.SelectClause).Exprs {
			typ, ok := ast.GetStaticallyKnownType(item.Expr.(*ast.CastExpr).Type)
			if !ok || typ.Width() != width {
				t.Errorf("%+v: %s read as %v", opts, ast.AsString(item.Expr), typ)
			}
		}
	}
}

func TestParseQueryStatements(t *testing.T) {
	for name, test := range map[string]struct {
		sql      string
		empty    bool
		multiple bool
	}{
		"empty":               {sql: "", empty: true},
		"comment only":        {sql: "-- nothing\n/* here */", empty: true},
		"semicolons only":     {sql: ";;", empty: true},
		"trailing semicolons": {sql: selectOne + ";;"},
		"two statements":      {sql: selectOne + "; SELECT 2", multiple: true},
		"leading semicolon":   {sql: ";" + selectOne},
		"comment after":       {sql: selectOne + "; -- done"},
		"parenthesized":       {sql: "(" + selectOne + ")"},
		"values":              {sql: "VALUES (1)"},
	} {
		t.Run(name, func(t *testing.T) {
			stmt, err := ParseQuery(test.sql, Options{})
			switch {
			case test.multiple:
				if !errors.Is(err, ErrMultipleStatements) {
					t.Fatalf("err = %v, want %v", err, ErrMultipleStatements)
				}
			case err != nil:
				t.Fatal(err)
			case test.empty != (stmt == nil):
				t.Fatalf("statement %v, want empty %v", stmt, test.empty)
			}
		})
	}
}

func TestSyntaxErrors(t *testing.T) {
	for name, test := range map[string]struct {
		sql, want string
		lexical   bool
	}{
		"bad token":         {sql: "SELECT 1 FROM WHERE", want: `at or near "where": syntax error`},
		"end of input":      {sql: "SELECT (", want: `at or near "EOF": syntax error`},
		"not a query":       {sql: "INSERT INTO t VALUES (1)", want: `at or near "insert": syntax error`},
		"unterminated":      {sql: "SELECT 'a", want: "lexical error: unterminated string", lexical: true},
		"unimplemented":     {sql: "SELECT 1 GROUP BY ROLLUP (1)", want: "unimplemented: rollup"},
		"nested array":      {sql: "SELECT 1::int[][]", want: "unimplemented: nested arrays"},
		"unsupported type":  {sql: "SELECT 1::money", want: "unimplemented: type name money"},
		"bad bit literal":   {sql: "SELECT B'102'", want: "is not a valid binary digit", lexical: true},
		"bad decimal scale": {sql: "SELECT 1::decimal(2, 3)", want: "scale (3) must be between 0 and precision (2)"},
		"bad bit length":    {sql: "SELECT 1::bit(0)", want: "length for type bit must be at least 1"},
		"bad float prec":    {sql: "SELECT 1::float(0)", want: "precision for type float must be at least 1 bit"},
		"big float prec":    {sql: "SELECT 1::float(60)", want: "precision for type float must be less than 54 bits"},
		"zero char length":  {sql: "SELECT 1::char(0)", want: "length for type CHAR must be at least 1"},
		"zero placeholder":  {sql: "SELECT $0", want: "placeholder index must be between 1 and", lexical: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseQuery(test.sql, Options{})
			var syntax *SyntaxError
			if !errors.As(err, &syntax) {
				t.Fatalf("err = %v, want a SyntaxError", err)
			}
			if syntax.Lexical != test.lexical || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %q (lexical %v), want %q", err, syntax.Lexical, test.want)
			}
		})
	}
}

func TestParseExpr(t *testing.T) {
	expr, err := ParseExpr("a + 1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ast.AsString(expr); got != "a + 1" {
		t.Errorf("printed %q", got)
	}
	for _, sql := range []string{
		"1 AS x", "1, 2", "1 FROM t", "1 WHERE true", "1 GROUP BY 1", "1 ORDER BY 1", "1 LIMIT 1",
		"DISTINCT 1", "1 UNION SELECT 2", "1 FOR UPDATE", "1; SELECT 2",
	} {
		if _, err := ParseExpr(sql, Options{}); err == nil {
			t.Errorf("%q read as an expression", sql)
		}
	}
	if _, err := ParseExpr("1 WINDOW w AS ()", Options{}); !errors.Is(err, ErrNotExpression) {
		t.Errorf("a window clause: err = %v", err)
	}
	if _, err := ParseExpr("(", Options{}); err == nil {
		t.Error("an incomplete expression parsed")
	}
}

func TestGoldenExpressions(t *testing.T) {
	var cases []string
	golden.LoadJSON(t, "expression_cases", &cases)
	got := make(map[string]string, len(cases))
	for _, expr := range cases {
		stmt, err := ParseQuery(selectPrefix+expr, Options{})
		if err != nil {
			got[expr] = errorPrefix + err.Error()
			continue
		}
		got[expr] = ast.AsString(stmt)
	}
	if *golden.Update {
		golden.WriteJSON(t, "expressions", got)
		return
	}
	var want map[string]string
	golden.LoadJSON(t, "expressions", &want)
	for _, expr := range cases {
		if got[expr] != want[expr] {
			t.Errorf("%q\n got: %s\nwant: %s", expr, got[expr], want[expr])
		}
	}
}

func TestParserPoolReleases(t *testing.T) {
	p := parserPool.Get().(*parser)
	if _, err := p.parseQuery(selectOne+" FROM t WHERE a IN (1, 2, 3)", Options{}); err != nil {
		t.Fatal(err)
	}
	if !p.release() {
		t.Fatal("a parser with a small token buffer was not pooled")
	}
	if len(p.tokens) != 0 || p.lexer.stmt != nil || p.scanner.in != "" || p.tokens[:1][0].str != "" {
		t.Error("a pooled parser kept the last statement")
	}
	big := &parser{tokens: make([]sqlSymType, 0, maxPooledTokens+1)}
	if big.release() {
		t.Error("a parser with an oversized token buffer was pooled")
	}
	var values strings.Builder
	for i := range maxPooledTokens {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("1")
	}
	if _, err := ParseQuery(selectOne+" WHERE a IN ("+values.String()+")", Options{}); err != nil {
		t.Fatal(err)
	}
}
