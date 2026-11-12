// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in licenses/BSD-vitess.txt.

// Portions of this file are additionally subject to the following
// license and copyright.
//
// Copyright 2015 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// This code was derived from https://github.com/youtube/vitess.

// Modified by The Trickster Authors in 2026: reduced to parsing one query or expression.

// Package pgparser parses one PostgreSQL-family query statement (SELECT, VALUES, TABLE and their
// WITH, set-operation and parenthesized forms) or one scalar expression into an ast tree. It parses
// no other kind of statement.
//
// The package is derived from github.com/cockroachdb/cockroachdb-parser at tag v0.23.2 (commit
// 4168c294f812), CockroachDB 23.2. Each file keeps its upstream notices; the Business Source License
// they name converted to the Apache License 2.0 on its Change Date, 2026-10-01. The licenses
// directory holds the BSD and PostgreSQL texts that the Vitess- and PostgreSQL-derived parts require.
package pgparser

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
)

// Options adjusts how a statement is read.
type Options struct {
	// NakedIntIsInt4 reads the bare INT and INTEGER type names as 4-byte integers, as PostgreSQL
	// defines them, instead of 8-byte ones.
	NakedIntIsInt4 bool
	// Dialect adds an engine's syntax to the grammar; nil parses the PostgreSQL family's alone.
	Dialect *Dialect
}

const (
	exprPrefix      = "SELECT "
	floatPrecSingle = 24
	floatPrecDouble = 54
	initialTokens   = 64
	// maxPooledTokens bounds the token buffer a pooled parser keeps, so one huge statement cannot pin memory
	maxPooledTokens = 4096
)

var (
	// ErrMultipleStatements indicates input holding more than one statement.
	ErrMultipleStatements = errors.New("expected 1 statement")
	// ErrNotExpression indicates ParseExpr input that is not a single scalar expression.
	ErrNotExpression = errors.New("expected 1 expression")
	// ErrStatementTooLong indicates a statement too long for the parser's 32-bit token offsets.
	ErrStatementTooLong = errors.New("statement exceeds the maximum length")

	errBitLengthNotPositive = errors.New("length for type bit must be at least 1")
	errFloatPrecAtLeast1    = errors.New("precision for type float must be at least 1 bit")
	errFloatPrecMax54       = errors.New("precision for type float must be less than 54 bits")
)

// parser holds the state of one parse. The zero value is ready to use.
type parser struct {
	scanner    scanner
	lexer      lexer
	parserImpl sqlParserImpl
	tokens     []sqlSymType
}

var parserPool = sync.Pool{New: func() any { return &parser{tokens: make([]sqlSymType, 0, initialTokens)} }}

// release clears the parser of the last statement and returns it to the pool unless its token
// buffer grew past maxPooledTokens; it reports whether the parser was pooled
func (p *parser) release() bool {
	if cap(p.tokens) > maxPooledTokens {
		return false
	}
	clear(p.tokens[:min(len(p.tokens)+1, cap(p.tokens))])
	p.tokens = p.tokens[:0]
	p.scanner = scanner{}
	p.lexer = lexer{}
	p.parserImpl = sqlParserImpl{}
	parserPool.Put(p)
	return true
}

// ParseQuery parses one query statement. Empty input yields a nil statement and no error.
func ParseQuery(sql string, opts Options) (*ast.Select, error) {
	p := parserPool.Get().(*parser)
	stmt, err := p.parseQuery(sql, opts)
	p.release()
	return stmt, err
}

// ParseExpr parses one scalar expression.
func ParseExpr(sql string, opts Options) (ast.Expr, error) {
	stmt, err := ParseQuery(exprPrefix+sql, opts)
	if err != nil {
		return nil, err
	}
	if stmt == nil || stmt.With != nil || stmt.OrderBy != nil || stmt.Limit != nil || stmt.Locking != nil {
		return nil, ErrNotExpression
	}
	clause, ok := stmt.Select.(*ast.SelectClause)
	if !ok || len(clause.Exprs) != 1 || clause.Exprs[0].As != "" || len(clause.From.Tables) != 0 ||
		clause.Where != nil || clause.GroupBy != nil || clause.Having != nil || clause.Window != nil ||
		clause.Distinct || clause.DistinctOn != nil {
		return nil, ErrNotExpression
	}
	return clause.Exprs[0].Expr, nil
}

func (p *parser) parseQuery(sql string, opts Options) (*ast.Select, error) {
	if len(sql) >= math.MaxInt32 {
		return nil, ErrStatementTooLong
	}
	nakedIntType := ast.TypeInt
	if opts.NakedIntIsInt4 {
		nakedIntType = ast.TypeInt4
	}
	p.initScanner(sql, opts.Dialect)
	var result *ast.Select
	for {
		stmtSQL, tokens, done := p.scanOneStmt()
		// an empty statement, such as the text after a trailing semicolon, is skipped
		if len(tokens) > 0 {
			stmt, err := p.parseStatement(stmtSQL, tokens, nakedIntType, opts.Dialect)
			if err != nil {
				return nil, err
			}
			if result != nil {
				return nil, ErrMultipleStatements
			}
			result = stmt
		}
		if done {
			break
		}
	}
	return result, nil
}

// scanOneStmt scans the tokens of the next statement; done reports the end of the input.
func (p *parser) initScanner(sql string, d *Dialect) {
	p.scanner.init(sql)
	if d != nil {
		p.scanner.durations, p.scanner.backticks = d.durations, d.backticks
	}
}

func (p *parser) parseStatement(sql string, tokens []sqlSymType, nakedIntType *ast.T, d *Dialect) (*ast.Select, error) {
	if !d.rewrites() {
		return p.parse(sql, tokens, nakedIntType)
	}
	tokens, changed := d.collapse(sql, tokens)
	stmt, err := p.parse(sql, tokens, nakedIntType)
	if err == nil || !changed {
		return stmt, err
	}
	// The base grammar may read a clause's opening word as a name, as in a column named align, so it
	// gets the statement when the dialect's reading fails. Scanning again gives it unshared literals.
	outer := p.scanner
	p.initScanner(sql, d)
	_, tokens, _ = p.scanOneStmt()
	p.scanner = outer
	if base, baseErr := p.parse(sql, tokens, nakedIntType); baseErr == nil {
		return base, nil
	}
	return nil, err
}

func (p *parser) scanOneStmt() (sql string, tokens []sqlSymType, done bool) {
	tokens = p.tokens[:0]
	tokens = append(tokens, sqlSymType{})
	lval := &tokens[0]

	// Scan the first token.
	for {
		p.scanner.scan(lval)
		if lval.id == 0 {
			return "", nil, true
		}
		if lval.id != ';' {
			break
		}
	}

	startPos := lval.pos
	// We make the resulting token positions match the returned string.
	lval.pos = 0
	lval.end = int32(p.scanner.pos) - startPos // #nosec G115 -- ParseQuery rejects statements of 2 GiB or more
	for {
		if lval.id == ERROR {
			p.tokens = tokens
			return p.scanner.in[startPos:], tokens, true
		}
		tokens = append(tokens, sqlSymType{})
		lval = &tokens[len(tokens)-1]
		p.scanner.scan(lval)
		if lval.id == 0 || lval.id == ';' {
			endPos := p.scanner.pos
			if lval.id == ';' {
				endPos--
			}
			tokens = tokens[:len(tokens)-1]
			p.tokens = tokens
			return p.scanner.in[startPos:endPos], tokens, lval.id == 0
		}
		lval.pos -= startPos
		lval.end = int32(p.scanner.pos) - startPos // #nosec G115 -- as above
	}
}

func (p *parser) parse(sql string, tokens []sqlSymType, nakedIntType *ast.T) (*ast.Select, error) {
	p.lexer.init(sql, tokens, nakedIntType)
	defer p.lexer.cleanup()
	if p.parserImpl.Parse(&p.lexer) != 0 {
		if p.lexer.lastError == nil {
			// This should never happen -- there should be an error object
			// every time Parse() returns nonzero. We're just playing safe
			// here.
			p.lexer.Error(syntaxErrorMessage)
		}
		return nil, p.lexer.lastError
	}
	return p.lexer.stmt, nil
}

// unaryNegation constructs an AST node for a negation. This attempts
// to preserve constant NumVals and embed the negative sign inside
// them instead of wrapping in an UnaryExpr. This in turn ensures
// that negative numbers get considered as a single constant
// for the purpose of formatting.
func unaryNegation(e ast.Expr) ast.Expr {
	if cst, ok := e.(*ast.NumVal); ok {
		cst.Negate()
		return cst
	}

	// Common case.
	return &ast.UnaryExpr{
		Operator: ast.MakeUnaryOperator(ast.UnaryMinus),
		Expr:     e,
	}
}

func newBitType(width int32, varying bool) (*ast.T, error) {
	if width < 1 {
		return nil, errBitLengthNotPositive
	}
	if varying {
		return ast.MakeTypeVarBit(width), nil
	}
	return ast.MakeTypeBit(width), nil
}

func newFloat(prec int64) (*ast.T, error) {
	if prec < 1 {
		return nil, errFloatPrecAtLeast1
	}
	if prec <= floatPrecSingle {
		return ast.TypeFloat4, nil
	}
	if prec <= floatPrecDouble {
		return ast.TypeFloat, nil
	}
	return nil, errFloatPrecMax54
}

func newDecimal(prec, scale int32) (*ast.T, error) {
	if scale > prec {
		return nil, fmt.Errorf("scale (%d) must be between 0 and precision (%d)", scale, prec)
	}
	return ast.MakeTypeDecimal(prec, scale), nil
}

func arrayOf(ref ast.ResolvableTypeReference) ast.ResolvableTypeReference {
	if typ, ok := ast.GetStaticallyKnownType(ref); ok {
		return ast.MakeTypeArray(typ)
	}
	return &ast.ArrayTypeReference{ElementType: ref}
}
