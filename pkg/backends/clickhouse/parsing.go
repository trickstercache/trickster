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

package clickhouse

import (
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/sqlparser"
)

// chParser implements a basic sql parser for clickhouse.
// it currently supports the base parsing of select queries needed for Trickster
// to determine if it is a time range, and if so, extracts that time range information.
type chParser struct {
	*sqlparser.Parser
}

var lexOpts = LexerOptions()
var lexer = lsql.NewLexer(lexOpts)
var parser = &chParser{
	Parser: sqlparser.New(
		parsing.New(nil, lexer, lexOpts).
			WithDecisions("FindVerb",
				parsing.DecisionSet{
					lsql.TokenWith: atWith,
				},
			).
			WithDecisions("SelectQueryKeywords",
				parsing.DecisionSet{
					tokenPreWhere: atPreWhere,
					tokenFormat:   atFormat,
				},
			),
	).(*sqlparser.Parser),
}

// parse parses the Time Range Query
func parse(statement string) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	trq := &timeseries.TimeRangeQuery{Statement: statement}
	ro := &timeseries.RequestOptions{}
	rs, err := parser.Run(sqlparser.NewRunContext(trq, ro), parser, trq.Statement)

	results := rs.Results()
	verb, ok := rs.GetResultsCollection("verb")
	var canObjectCache bool // indicates the query, while not a time series can be cached by the Object Proxy Cache
	if !ok {
		return nil, nil, false, sqlparser.ErrNotTimeRangeQuery
	}
	if vs, ok := verb.(string); ok {
		canObjectCache = vs == lsql.TokenValSelect
	}
	if err != nil {
		return nil, nil, canObjectCache, parsing.ParserError(err, rs.Current())
	}
	var t *token.Token
	if sql.HasLimitClause(results) {
		return nil, nil, canObjectCache, ErrLimitUnsupported
	}
	if t, err = parseGroupByTokens(results, trq, ro); err != nil {
		return nil, nil, canObjectCache, parsing.ParserError(err, t)
	}
	if t, err = parseSelectTokens(results, trq, ro); err != nil {
		return nil, nil, canObjectCache, parsing.ParserError(err, t)
	}
	if t, err = parseWhereTokens(results, trq, ro); err != nil {
		return nil, nil, canObjectCache, parsing.ParserError(err, t)
	}
	return trq, ro, canObjectCache, nil
}

// Tokens for String Interpolation
const (
	tkRange  = "<$RANGE$>"
	tkTS1    = "<$TS1$>"
	tkTS2    = "<$TS2$>"
	tkFormat = "<$FORMAT$>"
)

const day = time.Hour * 24
const week = day * 7

var tokenToStartOfLookup = map[string]time.Duration{
	"tomonday":                week,
	"tostartofweek":           week,
	"tostartofday":            day,
	"tostartofhour":           time.Hour,
	"tostartofminute":         time.Minute,
	"tostartoffiveminute":     time.Minute * 5,
	"tostartoftenminutes":     time.Minute * 10,
	"tostartoffifteenminutes": time.Minute * 15,
}

var tokenDurations = map[token.Typ]time.Duration{
	tokenWeek:   week,
	tokenDay:    day,
	tokenHour:   time.Hour,
	tokenMinute: time.Minute,
	tokenSecond: time.Second,
}

var supportedFormats = map[string]byte{
	"json":                          0,
	"csv":                           1,
	"csvwithnames":                  2,
	"tabseparated":                  3,
	"tsv":                           3,
	"tabseparatedwithnames":         4,
	"tsvwithnames":                  4,
	"tabseparatedwithnamesandtypes": 5,
	"tsvwithnamesandtypes":          5,
}

var timeFormats = map[int]byte{
	0:          0, // expect epoch seconds from clickhouse, convey seconds to requester
	1000:       1, // expect epoch milliseconds from clickhouse, convey milliseconds to requester
	1000000:    2, // expect epoch microseconds (u) from clickhouse, convey microseconds to requester
	1000000000: 3, // expect epoch nanoseconds from clickhouse, convey nanoseconds to requester
}

func atWith(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	if rs.Current().Typ != lsql.TokenWith {
		rs.WithError(sql.ErrNotAtWith)
		return nil
	}
	sp, ok := bp.(*chParser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	tl := make(token.Tokens, 0, 19)
	for {
		t := rs.Peek()
		if lsql.IsVerb(t.Typ) {
			break
		}
		rs.Next()
		if t.Typ == lsql.TokenComment || t.Typ == token.Space {
			continue
		}
		if t.Typ.IsEOF() || t.Typ.IsErr() {
			return nil
		}
		tl = append(tl, t)
	}
	trq, ro := sqlparser.ArtifactsFromContext(rs.Context())
	err := sp.parseWithTokens(rs, trq, ro, tl)
	if err != nil {
		rs.WithError(err)
		return nil
	}
	return sql.FindVerb
}

// parseWithTokens parses the withTokens list into a key=value map
func (p *chParser) parseWithTokens(rs *parsing.RunState, trq *timeseries.TimeRangeQuery,
	ro *timeseries.RequestOptions, tl token.Tokens) error {
	l := len(tl)
	// this puts the tokens between with and select into a map[variableName]value
	if l == 0 {
		return nil
	}
	if l < 3 {
		return ErrInvalidWithClause
	}
	// we must have a single group of 3, or groups of 4 after adding 1 to l
	// (to account for lack of trailing comma), otherwise
	// it may be something complex like: dictGetString('s', server, xxHash64(server)) as s
	// which we'll just skip out on
	if l != 3 && (l+1)%4 != 0 {
		return nil
	}
	wv := make(token.Lookup)
	var t *token.Token
	for i, v := range tl {
		// skips comma and AS from a list like: 'expr1' as var1 , 'expr2' as var2
		// which should always be in the odd indexes
		if i%2 != 0 {
			continue
		}
		// index 0, 4, 8, 12, etc., where v.Val is the value of the variable
		if i%4 == 0 {
			t = v
			continue
		}
		// index 2, 6, 10, 14, etc., where v.Val is the variable name (map key)
		wv[v.Val] = t
	}
	rs.SetResultsCollection("withVars", wv)
	return nil
}

func atPreWhere(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	p, ok := bp.(*chParser)
	if !ok {
		rs.WithError(parsing.ErrUnsupportedParser)
		return nil
	}
	rs.SetResultsCollection("preWhereTokens", p.GetFieldList(rs,
		tokenPreWhere, ErrNotAtPreWhere, token.IsLogicalOperator,
		sql.IsWhereBreakable, sql.DefaultIsContinuable, true))

	return rs.GetReturnFunc(parsing.StateUnexpectedToken, p.SelectQueryKeywords(), false)
}

func atFormat(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	var t *token.Token
	trq, ro := sqlparser.ArtifactsFromContext(rs.Context())
	for {
		t = rs.Next()
		if t.Typ.IsBreakable() || lsql.IsNonVerbPrimaryKeyword(t.Typ) {
			return nil
		}
		if t.Typ == lsql.TokenComment {
			sqlparser.ParseComment(rs)
			// get query-specific directives from comments here.
			continue
		}
		if t.Typ == token.Space {
			continue
		}
		break
	}
	fn, ok := supportedFormats[t.Val]
	if !ok {
		rs.WithError(ErrUnsupportedOutputFormat)
		return nil
	}
	ro.OutputFormat = fn
	trq.Statement = trq.Statement[:t.Pos] + tkFormat
	return scanForCommentsUntilEOF
}

func scanForCommentsUntilEOF(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	var t *token.Token
	for {
		t = rs.Next()
		if t.Typ.IsBreakable() {
			break
		}
		if t.Typ == lsql.TokenComment {
			sqlparser.ParseComment(rs)
		}
	}
	return nil
}

func parseSelectTokens(results map[string]interface{},
	trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) (*token.Token, error) {
	if results == nil {
		return nil, sqlparser.ErrMissingTimeseries
	}
	v, ok := results["selectTokens"]
	if !ok {
		return nil, sqlparser.ErrMissingTimeseries
	}
	st, ok := v.([]token.Tokens)
	if !ok {
		return nil, sqlparser.ErrMissingTimeseries
	}
	l := len(st)
	if l == 0 {
		return nil, sqlparser.ErrMissingTimeseries
	}
	var withVars token.Lookup
	v, ok = results["withVars"]
	if ok && v != nil {
		withVars, _ = v.(token.Lookup)
	}
	var foundTimeSeries bool
	for _, fieldParts := range st {
		var prev *token.Token
		var isTimeSeries, isIntDiv, needStep, checkMultiplier, expectBaseTimeField,
			isToStartOfInterval, expectAlias bool
		var d time.Duration
		var x int
		var err error
		if len(fieldParts) == 0 {
			continue
		}
		for _, t := range fieldParts {
			// this section uses a clone so we can manipulate search/replace on WITH vars
			// without impacting the token positioning and string length of the query
			if t.Typ == token.LeftParen || t.Typ == token.RightParen ||
				t.Typ == token.Space || t.Typ == lsql.TokenComment {
				continue
			}
			// this search/replaces any identified WITH variables before evaluating further
			if t.Typ == token.Identifier && withVars != nil {
				if v, ok := withVars[t.Val]; ok {
					t = v
				}
			}
			if isToStartOfInterval {
				if !isTimeSeries {
					ro.BaseTimestampFieldName = t.Val
					isTimeSeries = true
				}
				if prev.Typ == tokenInterval {
					x, err = getInt(t)
					if err != nil {
						return t, err
					}
				}
				if prev.Typ == token.Number && x > 0 {
					d, ok := tokenDurations[t.Typ]
					if !ok {
						return t, sqlparser.ErrStepParse
					}
					trq.Step = d * time.Duration(x)
					needStep = false
					isToStartOfInterval = false
					checkMultiplier = true
					foundTimeSeries = true
				}
				goto nextIteration
			}
			if isTimeSeries {
				if t.Typ == lsql.TokenAs {
					expectAlias = true
					goto nextIteration
				}
				if expectAlias {
					trq.TimestampDefinition.Name = t.Val
					break
				}
				if checkMultiplier {
					if prev.Typ == token.Multiply && t.Typ == token.Number {
						n, err := getInt(t)
						if err != nil {
							return t, err
						}
						b, ok := timeFormats[n]
						if !ok {
							return t, ErrUnsupportedOutputFormat
						}
						ro.TimeFormat = b
						trq.TimestampDefinition.ProviderData1 = int(b)
						checkMultiplier = false
					}
				}
				if needStep {
					if token.IsComma(prev.Typ) {
						n, err := getInt(t)
						if err != nil {
							return t, err
						}
						d = time.Duration(n) * time.Second
					}
					if prev.Typ == token.Multiply {
						n, err := getInt(t)
						if err != nil {
							return t, err
						}
						d2 := time.Duration(n) * time.Second
						if d != d2 {
							return t, sqlparser.ErrStepParse
						}
						trq.Step = d
						needStep = false
						checkMultiplier = true
					}
				}
				if expectBaseTimeField {
					ro.BaseTimestampFieldName = t.Val
					expectBaseTimeField = false
					needStep = isIntDiv
					checkMultiplier = !needStep
				}
			}
			if t.Typ == tokenToStartOfInterval {
				isToStartOfInterval = true
				goto nextIteration
			}
			if (prev != nil && prev.Typ == tokenIntDiv && t.Typ == tokenToInt32) ||
				((prev == nil || prev.Typ == tokenToInt32) && t.Typ == tokenToStartOf) {
				isTimeSeries = true
				foundTimeSeries = true
				isIntDiv = (prev != nil && prev.Typ == tokenIntDiv)
				expectBaseTimeField = true
				if t.Typ == tokenToStartOf {
					// get the step based on the exact ToStartOf function name
					d, ok := tokenToStartOfLookup[t.Val]
					if !ok {
						return t, ErrUnsupportedToStartOfFunc
					}
					trq.Step = d
				}
			}
		nextIteration:
			prev = t
		}
		if isTimeSeries && !expectAlias {
			last := fieldParts[len(fieldParts)-1]
			trq.TimestampDefinition.Name =
				trq.Statement[fieldParts[0].Pos : last.Pos+len(last.Val)]
		}
	}
	if !foundTimeSeries {
		return nil, sqlparser.ErrMissingTimeseries
	}
	return nil, nil
}

func getInt(t *token.Token) (int, error) {
	var n int
	var err error
	if t.Typ == token.Number {
		n, err = strconv.Atoi(t.Val)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	return 0, token.ErrParsingInt
}

// SolveMathExpression will solve a rudimentary integer math expression in the format of:
// $int $plusOrMinus $int [$plusOrMinus $int ...]
// parenthesis and other complications are not supported, but 'now()' is translated into
// an int64 of epoch seconds.
// It will iterate the tokens until the end of the expression is reached by encountering a
// conditional, eof, or error token, or a token that does not conform to the format above.
// Since the first token has likely already been parsed to a number value in order to
// need to call SolveMathExpression, that startValue is passed here so as to avoid a reparse
// unless startValue is <= 0.  the solved expression, the number of indexes in fieldParts
// advanced and the error state are returned
func SolveMathExpression(fieldParts token.Tokens, startValue int64,
	withVars token.Lookup) (int64, int, error) {
	var i, j int
	var v int64
	var t *token.Token
	prev := &token.Token{Typ: token.Plus}
	for i, t = range fieldParts {
		if i == 0 && startValue > 0 {
			v = startValue
			goto nextIteration
		}
		if t.Typ == token.LeftParen || t.Typ == token.RightParen ||
			t.Typ == token.Space || t.Typ == lsql.TokenComment {
			continue
		}
		if t.Typ.IsBreakable() || token.IsLogicalOperator(t.Typ) {
			i--
			break
		}
		if j%2 == 0 { // we're on an expected number
			// this search/replaces any identified WITH variables before evaluating further
			if t.Typ == token.Identifier && withVars != nil {
				if v, ok := withVars[t.Val]; ok {
					t = v
				}
			}
			x, err := t.Int64()
			if err != nil {
				return -1, i, err
			}
			if prev.Typ == token.Minus {
				x *= -1
			}
			v += x
			goto nextIteration
		}
		// otherwise we're on an expected operator
		if !t.Typ.IsAddOrSubtract() {
			return -1, i, parsing.ErrUnexpectedToken
		}
	nextIteration:
		prev = t
		j++
	}
	return v, i, nil
}

// This parses the WhereTokens list for any Time Ranges pertaining to the detected Timestamp Field
func parseWhereTokens(results map[string]interface{},
	trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) (*token.Token, error) {
	if ro == nil {
		return nil, nil
	}
	if results == nil {
		return nil, sqlparser.ErrNotTimeRangeQuery
	}
	v, ok := results["whereTokens"]
	if !ok {
		return nil, sqlparser.ErrNotTimeRangeQuery
	}
	wt, ok := v.([]token.Tokens)
	if !ok {
		return nil, sqlparser.ErrNotTimeRangeQuery
	}
	l := len(wt)
	// if there are any PREWHERE items, we need to prepend them to the whereTokens list
	if v, ok = results["preWhereTokens"]; ok {
		if pwt, ok := v.([]token.Tokens); ok && len(pwt) > 0 {
			pl := len(pwt)
			ml := pl
			if l > 0 {
				ml += 1 + l
			}
			mt := make([]token.Tokens, ml)
			copy(mt[:pl], pwt)
			if l > 0 {
				mt[pl] = token.Tokens{&token.Token{Typ: token.LogicalAnd, Val: "AND"}}
				copy(mt[pl+1:], wt)
			}
			wt = mt
			l = len(wt)
		}
	}
	// where tokens length should always be odd, since conditions are stitched with conjunctions
	// this logical and comparison will verify l is odd while also naturally covering the non-zero check
	if l&1 != 1 {
		return nil, sqlparser.ErrTimerangeParse
	}
	var withVars token.Lookup
	v, ok = results["withVars"]
	if ok && v != nil {
		withVars, _ = v.(token.Lookup)
	}
	var e timeseries.Extent
	var s1, e1, s2, e2 int
	var tsr1, tsr2 string
	var isBetween bool
	for n, fieldParts := range wt {
		var atLowerBound bool
		var state int
		var prev *token.Token
		var i int
		// c-style iteration, rather than ranging, allows passing a subslice of fieldParts to other funcs for
		// processing, while also advancing the iterator
		lfp := len(fieldParts)
		for i = 0; i < lfp; i++ {
			t := fieldParts[i]
			if t.Typ == token.LeftParen || t.Typ == token.RightParen ||
				t.Typ == token.Space || t.Typ == lsql.TokenComment || t.Typ == tokenToDateFunc {
				continue
			}
			if t.Typ.IsBreakable() {
				break
			}
			// this search/replaces any identified WITH variables before evaluating further
			if t.Typ == token.Identifier && withVars != nil {
				if v, ok := withVars[t.Val]; ok {
					t = v
				}
			}
		sw:
			switch state {
			case 0: // confirms the base timestamp field name and abandons the set if it is not
				if t.Val != ro.BaseTimestampFieldName && t.Val != trq.TimestampDefinition.Name {
					goto nextSet // skip conditions in the where clause unrelated to time series
				}
				state++
			case 1: // verifies we are comparing the bstfn to a time via BETWEEN or a <,>,>=,<= operator
				isBetween = isBetween || t.Typ == lsql.TokenBetween
				if !isBetween && !t.Typ.IsGreaterOrLessThan() {
					return t, parsing.ErrUnexpectedToken
				}
				atLowerBound = isBetween || (t.Typ == token.GreaterThan ||
					t.Typ == token.GreaterThanOrEqual)
				state++
			case 2: // gets the first time and runs it through the evaluator
				ts, err := parseTimeField(t)
				if err != nil {
					return t, err
				}
				v, j, _ := SolveMathExpression(fieldParts[i:], ts, withVars)
				// if the comparator is > or <, rather than >= or <=, then the timerange is _not_ inclusive.
				// adjust the time boundary by one point in the appropriate time direction
				if prev.Typ == token.GreaterThan {
					v += int64(trq.Step.Seconds())
				} else if prev.Typ == token.LessThan {
					v -= int64(trq.Step.Seconds())
				}
				if atLowerBound {
					//e.Start = time.Unix(v, 0)
					e.Start, _, _ = lsql.TokenToTime(t)
					tsr1 = t.Val
					atLowerBound = false
				} else {
					//e.End = time.Unix(v, 0)
					e.End, _, _ = lsql.TokenToTime(t)
					tsr2 = t.Val
				}
				if s1 == 0 {
					s1 = fieldParts[0].Pos
					e1 = fieldParts[lfp-1].Pos + len(fieldParts[lfp-1].Val)
				} else {
					s2 = wt[n-1][0].Pos
					e2 = fieldParts[lfp-1].Pos + len(fieldParts[lfp-1].Val)
				}
				i += j
				state++
			case 3:
				// if t is AND, skip ahead, the next iteration will start a new
				// fieldParts unless the operator is BETWEEN ...
				if t.Typ == token.LogicalAnd {
					break sw
				}
				// therefore, if we make it to here, it MUST be a BETWEEN
				ts, err := parseTimeField(t)
				if err != nil {
					return t, err
				}
				v, j, _ := SolveMathExpression(fieldParts[i:], ts, withVars)
				// since we must be in a BETWEEN to be here, this must be the upper bound
				e.End = time.Unix(v, 0)
				tsr2 = t.Val
				i += j
				state++
			}
			prev = t
		}
	nextSet:
	}
	if e.Start.IsZero() {
		return nil, sqlparser.ErrNoLowerBound
	}
	if isBetween && e.End.IsZero() {
		return nil, sqlparser.ErrNoUpperBound
	}
	if e.End.IsZero() {
		e.End = time.Now()
	}
	trq.Extent = e
	var r1, r2 string
	if s1 > 0 && e1 > s1 {
		r1 = trq.Statement[s1:e1]
	}
	if s2 > 0 && e2 > s2 {
		r2 = trq.Statement[s2:e2]
	}
	if r1 != "" {
		trq.Statement = strings.Replace(trq.Statement, r1, tkRange, -1)
	}
	if r2 != "" {
		trq.Statement = strings.Replace(trq.Statement, r2, "", -1)
	}
	if tsr1 != "" {
		trq.Statement = strings.Replace(trq.Statement, tsr1, tkTS1, -1)
	}
	if tsr2 != "" {
		trq.Statement = strings.Replace(trq.Statement, tsr2, tkTS2, -1)
	}
	return nil, nil
}

func parseTimeField(t *token.Token) (int64, error) {
	ts, format, err := lsql.TokenToTime(t)
	if err != nil {
		return -1, err
	}
	if format == 1 {
		return ts.UnixNano() / 1000000, nil
	}
	return ts.Unix(), nil
}

func parseGroupByTokens(results map[string]interface{},
	trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) (*token.Token, error) {
	v, ok := results["groupByTokens"]
	if !ok {
		return nil, lsql.ErrInvalidGroupByClause
	}
	gbt, ok := v.(token.Tokens)
	if !ok || len(gbt) == 0 {
		return nil, lsql.ErrInvalidGroupByClause
	}
	trq.TagFieldDefintions = make([]timeseries.FieldDefinition, len(gbt))
	for i, v := range gbt {
		trq.TagFieldDefintions[i].Name = v.Val
	}
	return nil, nil
}
