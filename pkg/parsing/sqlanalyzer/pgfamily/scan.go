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
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
)

type scanKind uint8

const (
	scanSubquery scanKind = iota
	scanWindow
	scanVolatile
	scanUnsafe
	scanSuffix
)

// exprScan finds one kind of node in expression trees, without the per-walk closures and visitors
// separate walks would allocate; one is allocated per analysis and reused
type exprScan struct {
	kind       scanKind
	found      bool
	isVolatile func(string) bool
}

func (s *exprScan) VisitPre(expr ast.Expr) (bool, ast.Expr) {
	if s.found {
		return false, expr
	}
	switch node := expr.(type) {
	case *ast.Subquery:
		s.found = s.kind == scanSubquery
	case *ast.FuncExpr:
		switch s.kind {
		case scanWindow:
			s.found = node.WindowDef != nil
		case scanSuffix:
			s.found = node.Suffix != nil
		case scanVolatile:
			name := node.Func.Name()
			name = name[strings.LastIndexByte(name, '.')+1:]
			s.found = isVolatileFunction(strings.ToLower(name), s.isVolatile)
		}
	case *ast.OrExpr, *ast.NotExpr:
		s.found = s.kind == scanUnsafe
	case *ast.RangeCond:
		s.found = s.kind == scanUnsafe && node.Not
	case *ast.ComparisonExpr:
		s.found = s.kind == scanUnsafe && node.Operator.Symbol == ast.NE
	}
	return !s.found, expr
}

func (*exprScan) VisitPost(expr ast.Expr) ast.Expr { return expr }

func (s *exprScan) in(kind scanKind, exprs ...ast.Expr) bool {
	s.kind, s.found = kind, false
	for _, expr := range exprs {
		if expr != nil && !s.found {
			ast.WalkExpr(s, expr)
		}
	}
	return s.found
}

func (s *exprScan) inItems(kind scanKind, items ast.SelectExprs) bool {
	s.kind, s.found = kind, false
	for _, item := range items {
		if item.Expr != nil && !s.found {
			ast.WalkExpr(s, item.Expr)
		}
	}
	return s.found
}

func containsSubquery(s *exprScan, clause *ast.SelectClause) bool {
	if s.inItems(scanSubquery, clause.Exprs) {
		return true
	}
	if clause.Where != nil && s.in(scanSubquery, clause.Where.Expr) {
		return true
	}
	if s.in(scanSubquery, clause.GroupBy...) {
		return true
	}
	for _, table := range clause.From.Tables {
		if aliased, ok := table.(*ast.AliasedTableExpr); ok {
			if _, sub := aliased.Expr.(*ast.Subquery); sub {
				return true
			}
		}
	}
	return false
}

func containsSuffix(s *exprScan, stmt *ast.Select, clause *ast.SelectClause) bool {
	// subqueries are rejected before this runs, so the top-level clauses are all there is to read
	if s.inItems(scanSuffix, clause.Exprs) || s.in(scanSuffix, clause.GroupBy...) {
		return true
	}
	if clause.Where != nil && s.in(scanSuffix, clause.Where.Expr) {
		return true
	}
	for _, order := range stmt.OrderBy {
		if s.in(scanSuffix, order.Expr) {
			return true
		}
	}
	return false
}

type sqlTimeLayout struct {
	layout string
	style  boundStyle
}

var (
	sqlDateLayouts     = []sqlTimeLayout{{"2006-01-02", boundSQLDate}}
	sqlDateTimeLayouts = []sqlTimeLayout{
		{"2006-01-02 15:04:05.999999999", boundSQLDateTime}, {"2006-01-02 15:04:05", boundSQLDateTime},
	}
	rfc3339Layouts = []sqlTimeLayout{{time.RFC3339Nano, boundRFC3339}, {time.RFC3339, boundRFC3339}}
)

const dateLen = len("2006-01-02")

func parseSQLTime(value string) (time.Time, boundStyle, bool) {
	// the character after the date picks the only layouts that can match, so no attempt fails
	var layouts []sqlTimeLayout
	switch {
	case len(value) == dateLen:
		layouts = sqlDateLayouts
	case len(value) > dateLen && value[dateLen] == ' ':
		layouts = sqlDateTimeLayouts
	case len(value) > dateLen && value[dateLen] == 'T':
		layouts = rfc3339Layouts
	}
	for _, entry := range layouts {
		parsed, err := time.ParseInLocation(entry.layout, value, time.UTC)
		if err == nil {
			return parsed, entry.style, true
		}
	}
	return time.Time{}, 0, false
}
