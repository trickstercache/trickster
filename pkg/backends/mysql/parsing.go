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

package mysql

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/mysql/model"
	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/sqlparser"
)

var parser = sqlparser.New(parsing.New(nil, lexer, lopts))

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
	if sql.HasLimitClause(results) {
		return nil, nil, canObjectCache, ErrLimitUnsupported
	}
	err = parseTSColumn(rs, trq, ro)
	if err != nil {
		return nil, nil, false, err
	}
	err = parseFromTokens(rs, trq, ro)
	if err != nil {
		return nil, nil, false, err
	}
	err, canCache := parseWhereTokens(rs, trq, ro)
	// We only want to allow object caching if something is a select statement and otherwise
	// follows caching rules
	canObjectCache = canObjectCache && canCache
	if err != nil {
		return nil, nil, false, err
	}
	err = parseGroupByTokens(rs, trq, ro)
	if err != nil {
		return nil, nil, false, err
	}
	//var t *token.Token
	/*
		if t, err = parseGroupByTokens(results, trq, ro); err != nil {
			return nil, nil, canObjectCache, parsing.ParserError(err, t)
		}
		if t, err = parseSelectTokens(results, trq, ro); err != nil {
			return nil, nil, canObjectCache, parsing.ParserError(err, t)
		}
		if t, err = parseWhereTokens(results, trq, ro); err != nil {
			return nil, nil, canObjectCache, parsing.ParserError(err, t)
		}
	*/
	return trq, ro, canObjectCache, nil
}

// Parse the timeseries column out of the runstate.
// Returns an error if unable to find a timeseries column; this indicates a poorly constructed query.
func parseTSColumn(rs *parsing.RunState, trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) error {
	selectTokens, ok := rs.GetResultsCollection("selectTokens")
	if !ok {
		return sqlparser.ErrMissingTimeseries
	}
	tokens, ok := selectTokens.([]token.Tokens)
	if !ok {
		return sqlparser.ErrMissingTimeseries
	}
	fmt.Println(tokens)
	// Parse the first "statement" in the select portion. Need to check for datatype, column, alias
	stmnt := tokens[0]
	var col, alias string
	var foundCol, foundAlias, useAlias bool
	for i := 0; i < len(stmnt); i++ {
		t := stmnt[i]
		if t.Typ == token.Typ(66111) {
			useAlias = true
			continue
		}
		if t.Typ != token.Identifier {
			continue
		}
		if _, isDT := model.IsDataType(t); isDT {
			continue
		}
		if _, isAF := model.IsAggregateFunction(t); isAF {
			continue
		}
		if !foundCol {
			col = t.Val
			foundCol = true
		}
		if foundCol && useAlias {
			alias = t.Val
			foundAlias = true
		}
	}
	// Set in trq/ro
	if foundCol {
		trq.TimestampDefinition.Name = col
	}
	if foundAlias {
		ro.BaseTimestampFieldName = alias
	} else {
		ro.BaseTimestampFieldName = col
	}
	return nil
}

// Parse tokens related to the "from" part of the query.
// This should really just not fail.
func parseFromTokens(rs *parsing.RunState, trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) error {
	fromTokens, ok := rs.GetResultsCollection("fromTokens")
	if !ok {
		return sqlparser.ErrMissingTimeseries
	}
	_, ok = fromTokens.([]token.Tokens)
	if !ok {
		return sqlparser.ErrMissingTimeseries
	}
	return nil
}

// Parse tokens related to the "where" portion of the query.
// Queries MUST include a start clause with the tscol, and may OPTIONALLY include an end clause. Function
// returns an error where a timeseries range query could not be parsed from the WHERE portion. Function also
// returns a boolean value representing if the WHERE portion indicates a cacheable query, which is false if
// the query could not be parsed properly OR if there's some WHERE statement including the tscol that is unrelated
// to the timerange being requested.
func parseWhereTokens(rs *parsing.RunState, trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) (error, bool) {
	whereTokens, ok := rs.GetResultsCollection("whereTokens")
	if !ok {
		return sqlparser.ErrMissingTimeseries, false
	}
	tokens, ok := whereTokens.([]token.Tokens)
	if !ok {
		return sqlparser.ErrMissingTimeseries, false
	}
	tscol := ro.BaseTimestampFieldName
	var start, end time.Time
	var startInclusive, endInclusive bool
	var hasStart, hasEnd bool
	var canCache bool = true
	var err error
	// Run through statements to parse out start/end
	for idxStatement := 0; idxStatement < len(tokens); idxStatement++ {
		statement := tokens[idxStatement]
		if !hasStart && isTimeseriesStartStatement(statement, tscol) {
			start, _, err = lsql.TokenToTime(statement[2])
			if err != nil {
				return sqlparser.ErrNoLowerBound, false
			}
			if statement[2].Typ.IsOrEquals() {
				startInclusive = true
			}
			hasStart = true
		} else if !hasStart && isTimeseriesBetweenStatement(statement, tscol) {
			start, _, err = lsql.TokenToTime(statement[2])
			if err != nil {
				return sqlparser.ErrNoLowerBound, false
			}
			end, _, err = lsql.TokenToTime(statement[4])
			if err != nil {
				return sqlparser.ErrNoUpperBound, false
			}
			hasStart = true
			hasEnd = true
			break
		} else if hasStart && isTimeseriesEndStatement(statement, tscol) {
			end, _, err = lsql.TokenToTime(statement[2])
			if err != nil {
				return sqlparser.ErrNoUpperBound, false
			}
			if statement[2].Typ.IsOrEquals() {
				endInclusive = true
			}
			hasEnd = true
			break
		} else if statementContainsColumn(statement, tscol) {
			canCache = false
		}
	}
	// Use parsed info to update TRQ/RO
	// Check for error conditions first
	if !hasStart {
		return sqlparser.ErrNoLowerBound, false
	}
	if !hasEnd {
		end = time.Now()
	}
	// Account for inclusivity; if not OrEqualTo, we need to adjust by the TRQ step.
	if !startInclusive {
		start = start.Add(trq.Step)
	}
	if !endInclusive {
		end = end.Add(trq.Step * -1)
	}
	trq.Extent = timeseries.Extent{
		Start: start,
		End:   end,
	}
	return nil, canCache
}

func parseGroupByTokens(rs *parsing.RunState, trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions) error {
	gbTokens, ok := rs.GetResultsCollection("groupByTokens")
	if !ok {
		return lsql.ErrInvalidGroupByClause
	}
	tokens, ok := gbTokens.(token.Tokens)
	if !ok || len(tokens) == 0 {
		return lsql.ErrInvalidGroupByClause
	}
	var nextStep, hasStep bool
	trq.TagFieldDefintions = make([]timeseries.FieldDefinition, len(tokens))
	for i, v := range tokens {
		trq.TagFieldDefintions[i].Name = v.Val
		if strings.ToLower(v.Val) == "div" {
			nextStep = true
			continue
		}
		if nextStep {
			step, err := strconv.ParseInt(v.Val, 10, 64)
			if err != nil {
				return fmt.Errorf("failed to parse step from query: %s", err)
			}
			trq.Step = time.Duration(step) * time.Second
			hasStep = true
			nextStep = false
		}
	}
	if !hasStep {
		return fmt.Errorf("queries require a step; use GROUP BY tscol DIV (step in seconds)")
	}
	return nil
}

// TIMESERIES STUFF

func isTimeseriesStartStatement(statement token.Tokens, forColumn string) bool {
	return len(statement) == 3 &&
		(statement[0].Typ == token.Identifier && statement[0].Val == forColumn) &&
		(statement[1].Typ.IsGTorGTE()) &&
		(statement[2].Typ == token.Number)
}

func isTimeseriesEndStatement(statement token.Tokens, forColumn string) bool {
	return len(statement) == 3 &&
		(statement[0].Typ == token.Identifier && statement[0].Val == forColumn) &&
		(statement[1].Typ.IsLTorLTE()) &&
		(statement[2].Typ == token.Number)
}

func isTimeseriesBetweenStatement(statement token.Tokens, forColumn string) bool {
	return len(statement) == 5 &&
		(statement[0].Typ == token.Identifier && statement[0].Val == forColumn) &&
		(statement[1].Val == "between") &&
		(statement[2].Typ == token.Number) &&
		(statement[3].Typ == token.LogicalAnd) &&
		(statement[4].Typ == token.Number)
}

func statementContainsColumn(statement token.Tokens, forColumn string) bool {
	for _, t := range statement {
		if t.Val == forColumn {
			return true
		}
	}
	return false
}
