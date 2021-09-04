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

// Package sql provides a basic SQL SQLParser that can be extended by individual implementations.
// Because Trickster is a proxy/cache for time series, this package currently only has base
// support for specific SELECT statements
//
// NOTE: While Trickster does not proved a true, full AST; we would love to and welcome all contributions
//
package sql

import (
	"context"
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// ErrUnsupportedVerb represents an error of type "unsupported verb"
var ErrUnsupportedVerb = errors.New("unsupported verb")

// ErrUnsupportedClause represents an error of type "unsupported clause"
var ErrUnsupportedClause = errors.New("unsupported clause")

// // Parser defines the SQL Parser Interface
// type Parser interface {
// 	Run(parsing.Parser, string, context.Context) error
// }

// Parser represents the base SQLParser struct that conforms to the Parser interface
type Parser struct {
	options     *parsing.Options
	fvDecisions parsing.DecisionSet // FindVerb DecisionSet (SELECT, UPDATE, TRUNCATE, etc.)
	skDecisions parsing.DecisionSet // SelectKeyword DecisionSet (SELECT, FROM, WHERE, etc.)
}

// New returns a new SQL Parser, customized with the provided options
func New(po *parsing.Options) parsing.Parser {
	if po == nil {
		po = &parsing.Options{}
	}
	if po.EntryFunc() == nil {
		po = po.WithEntryFunc(FindVerb)
	}
	p := &Parser{
		options: po,
	}
	d := baseFindVerbDecisions()
	d2 := po.GetDecisions("FindVerb")
	for k, v := range d2 {
		if v == nil {
			delete(d, k)
			continue
		}
		d[k] = v
	}
	p.fvDecisions = d
	d = baseSelectKeywordDecisions()
	d2 = po.GetDecisions("SelectQueryKeywords")
	for k, v := range d2 {
		if v == nil {
			delete(d, k)
			continue
		}
		d[k] = v
	}
	p.skDecisions = d
	return p
}

func baseFindVerbDecisions() parsing.DecisionSet {
	return parsing.DecisionSet{
		token.Space:        FindVerb,
		lsql.TokenComment:  FindVerb,
		lsql.TokenSelect:   AtSelect,
		lsql.TokenInsert:   UnsupportedVerb,
		lsql.TokenUpdate:   UnsupportedVerb,
		lsql.TokenDelete:   UnsupportedVerb,
		lsql.TokenTruncate: UnsupportedVerb,
		lsql.TokenAlter:    UnsupportedVerb,
		lsql.TokenCreate:   UnsupportedVerb,
		lsql.TokenWith:     UnsupportedClause,
	}
}

func baseSelectKeywordDecisions() parsing.DecisionSet {
	return parsing.DecisionSet{
		lsql.TokenSelect:  AtSelect,
		lsql.TokenFrom:    AtFrom,
		lsql.TokenWhere:   AtWhere,
		lsql.TokenGroupBy: AtGroupBy,
		lsql.TokenHaving:  AtHaving,
		lsql.TokenOrderBy: AtOrderBy,
		lsql.TokenLimit:   AtLimit,
		lsql.TokenUnion:   AtUnion,
	}
}

// Options returns the SQL Parser options
func (sp *Parser) Options() *parsing.Options {
	return sp.options
}

// Run runs the SQL Parser
func (sp *Parser) Run(ctx context.Context, p parsing.Parser,
	query string) (*parsing.RunState, error) {
	lexer, _ := sp.options.Lexer()
	if lexer == nil {
		return nil, parsing.ErrNoLexer
	}
	rs := parsing.NewRunState(ctx)
	go lexer.Run(query, rs.Tokens())
	for state := sp.options.EntryFunc(); state != nil; {
		state = state(p, sp, rs)
	}
	return rs, rs.Error()
}

// UnsupportedVerb aborts the Parser with an Unsupported Verb error
func UnsupportedVerb(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	rs.WithError(ErrUnsupportedVerb)
	return nil
}

// UnsupportedClause aborts the Parser with an Unsupported Clause error
func UnsupportedClause(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	rs.WithError(ErrUnsupportedClause)
	return nil
}

// FindVerb will find the SQL Verb (SELECT, INSERT, etc.), bypassing any
// spaces or comments
func FindVerb(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.Next()
	return rs.GetReturnFunc(parsing.StateUnexpectedToken, p.fvDecisions, false)
}
