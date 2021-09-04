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

package sql

import (
	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// this file contains functions used while parsing select statements

// SelectTokens returns the parsed tokens grouped into the SELECT clause
func SelectTokens(rs *parsing.RunState) []token.Tokens {
	v, ok := rs.GetResultsCollection("selectTokens")
	if !ok {
		return nil
	}
	if t, ok := v.([]token.Tokens); ok {
		return t
	}
	return nil
}

// SelectQueryKeywords returns the SelectQueryKeywords DecisionSet
func (p *Parser) SelectQueryKeywords() parsing.DecisionSet {
	return p.skDecisions
}

// GetColumnsList returns a list of columns following a keyword and terminated by a keywords or EOF
// It omits the comma delimiter, and includes only the field identifier tokens
// This works for getting columns in GROUP BY clauses.
// RunState.Current() is expected to be on the sql keyword just prior to the start of its list
func (p *Parser) GetColumnsList(rs *parsing.RunState) token.Tokens {
	var t *token.Token
	tokens := make(token.Tokens, 0, 32)
	for {
		t = rs.Next()
		if t.Typ.IsBreakable() || lsql.IsPrimaryKeyword(t.Typ) {
			break
		}
		if t.Typ == token.Comma || t.Typ == token.Space {
			continue
		}
		tokens = append(tokens, t)
	}
	return tokens
}

// GetField returns a list of tokens associated with the Field or Phrase,
// omitting non-conjuctive delimiters (e.g., comma) not enclosed in parenthesis.
// The returned Tokens adds any logical operator delimiter as the last element.
func GetField(
	rs *parsing.RunState,
	isDelim, isBreakable, isContinuable token.TypeCheckFunc,
	isolateDelimiter bool,
) (token.Tokens, bool) {
	fieldParts := make(token.Tokens, 0, 16)
	var n *token.Token
	var pd int
	for i := 0; ; i++ {
		n = rs.Peek()
		if i > 0 && pd == 0 && isDelim(n.Typ) {
			if !isolateDelimiter {
				n = rs.Next()
			}
			break
		}
		n = rs.Next()
		if isolateDelimiter && i == 0 && isDelim(n.Typ) {
			return token.Tokens{n}, true
		}
		if n.Typ <= token.EOF {
			break
		}
		if isContinuable(n.Typ) { // usu if space or comment
			continue
		}
		if n.Typ == token.LeftParen {
			pd++
		}
		if n.Typ == token.RightParen {
			pd--
		}
		if pd == 0 && isBreakable(n.Typ) {
			// in this case, we don't advance the RunState, since it's probably a KW
			break
		}
		fieldParts = append(fieldParts, n)
	}
	return fieldParts, isDelim(n.Typ)
}

// GetFieldList gets a list of fields. A field is considered a list of tokens that comprises
// the definition of a single output field/column in the result. It expects RunState to be at the keyword
// just prior to the start of the
func (p *Parser) GetFieldList(rs *parsing.RunState, allowedToken token.Typ, disAllowedTokenErr error,
	isDelim, isBreakable, isContinuable token.TypeCheckFunc,
	isolateDelimiter bool) []token.Tokens {
	if rs.Current().Typ != allowedToken {
		rs.WithError(disAllowedTokenErr)
		return nil
	}
	t := make([]token.Tokens, 0, 8)
	for parts, more := GetField(rs,
		isDelim, isBreakable, isContinuable, isolateDelimiter); len(parts) > 0; parts,
		more = GetField(rs, isDelim, isBreakable, isContinuable, isolateDelimiter) {
		t = append(t, parts)
		if !more {
			break
		}
	}
	return t
}

// AtSelect is the state where the current item is of type TokenSelect
// it will parse the incoming items into a list of item lists that comprise
// the fields in the clause
func AtSelect(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	rs.SetResultsCollection("verb", lsql.TokenValSelect)
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	if rs.Current().Typ != lsql.TokenSelect {
		rs.WithError(ErrNotAtSelect)
		return nil
	}
	rs.SetResultsCollection("selectTokens", p.GetFieldList(rs, lsql.TokenSelect, ErrNotAtSelect,
		token.IsComma, DefaultIsBreakable, DefaultIsContinuable, false))
	if rs.Current().Typ != lsql.TokenFrom {
		rs.WithError(ErrNotAtFrom)
		return nil
	}
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// DefaultIsBreakable returns true if the token is a loop-breakable token - a primary keyword.
// These indicate the parser should break any loops and start a new token collection
func DefaultIsBreakable(t token.Typ) bool {
	return lsql.IsNonVerbPrimaryKeyword(t)
}

// IsWhereBreakable returns true if the token is a loop-breakable token (EOF or Error), or
// a primary keyword excluding BETWEEN.
//  These indicate the parser should break any loops and start a new token collection
func IsWhereBreakable(t token.Typ) bool {
	return lsql.IsNonVerbPrimaryKeyword(t) && t != lsql.TokenBetween
}

// DefaultIsContinuable returns true if the token is irrelevant to the parsing of the base query
// such as a Space or Comment
func DefaultIsContinuable(t token.Typ) bool {
	return t == token.Space || t == lsql.TokenComment
}

// IsFromFieldDelimiterType returns true if the provided Typ is a FROM delimiter
func IsFromFieldDelimiterType(t token.Typ) bool {
	return t == token.Comma
}

// AtFrom is the state where the current item is of type TokenFrom
func AtFrom(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.SetResultsCollection("fromTokens", p.GetFieldList(rs, lsql.TokenFrom, ErrNotAtFrom,
		token.IsComma, DefaultIsBreakable, DefaultIsContinuable, false))
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtWhere is the state where the current item is of type TokenWhere
func AtWhere(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	fl := p.GetFieldList(rs, lsql.TokenWhere, ErrNotAtWhere,
		token.IsLogicalOperator, IsWhereBreakable, DefaultIsContinuable, true)
	lfl := len(fl)
	fl2 := make([]token.Tokens, 0, lfl)
	for i, tokens := range fl {
		ltk := len(tokens)
		if ltk > 0 {
			j := i + 1
			k := j + 1
			if ltk > 2 && tokens[1].Typ == lsql.TokenBetween && k < lfl {
				// this is a BETWEEN token, and we need to merge the contents
				// of the next 2 Tokens with this one, since GetFieldList broke
				// on the AND condition of the BETWEEN clause
				tokens = append(append(tokens, fl[j]...), fl[k]...)
				fl[j] = nil
				fl[k] = nil
			}
			fl2 = append(fl2, tokens)
		}
	}
	rs.SetResultsCollection("whereTokens", fl2)
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtGroupBy is the state where the current item is of type TokenGroupBy
func AtGroupBy(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	if rs.Current().Typ != lsql.TokenGroupBy {
		rs.WithError(ErrNotAtGroupBy)
		return nil
	}
	cl := p.GetColumnsList(rs)
	rs.SetResultsCollection("groupByTokens", cl)
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtHaving is the state where the current item is of type TokenHaving
func AtHaving(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.SetResultsCollection("havingTokens", p.GetFieldList(rs, lsql.TokenHaving, ErrNotAtHaving,
		token.IsLogicalOperator, DefaultIsBreakable, DefaultIsContinuable, true))
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtOrderBy is the state where the current item is of type TokenOrderBy
func AtOrderBy(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.SetResultsCollection("orderByTokens", p.GetFieldList(rs, lsql.TokenOrderBy, ErrNotAtOrderBy,
		token.IsComma, DefaultIsBreakable, DefaultIsContinuable, false))
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtUnion is the state where the current item is of type TokenUnion
// Trickster does not currently support queries with Union (we only need to look
// at the first query to get the structure and time series info we need, but we
// welcome all help in extending this support!)
func AtUnion(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	rs.WithError(parsing.ErrUnsupportedKeyword)
	return nil
}

// AtLimit is the state where the current item is of type TokenLimit
func AtLimit(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := ip.(*Parser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.SetResultsCollection("limitTokens", p.GetFieldList(rs, lsql.TokenLimit, ErrNotAtLimit,
		token.IsComma, DefaultIsBreakable, DefaultIsContinuable, false))
	return rs.GetReturnFunc(nil, p.skDecisions, false)
}

// AtIntoOutfile is the state where the current item is of type TokenIntoOutfile
func AtIntoOutfile(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	rs.WithError(parsing.ErrUnsupportedKeyword)
	return nil
}

// HasLimitClause returns true if the limitTokens entry is not nil
func HasLimitClause(results map[string]interface{}) bool {
	_, ok := results["limitTokens"]
	return ok
}
