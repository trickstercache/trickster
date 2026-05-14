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
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	psql "github.com/trickstercache/trickster/v2/pkg/parsing/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/sqlparser"
	ts "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// Query holds the parsed and tokenized SQL query
type Query struct {
	TokenizedStatement string
	// BaseTimestampFieldName is the underlying time column referenced inside a
	// date_bin/date_trunc bucketing expression. When a SELECT alias is used
	// (e.g. `date_bin(..., t) AS bucket`), the alias cannot appear in WHERE,
	// so SetExtent must re-emit predicates against the base column instead.
	BaseTimestampFieldName string
}

// V3InfluxQLQuery marks a parsed InfluxQL query as originating from the v3
// native endpoint (`/api/v3/query_influxql`). Serialization and SetExtent use
// this to route through the v3 JSON format while reusing the v1 InfluxQL parser.
type V3InfluxQLQuery struct {
	Inner any // *influxql.Query from the v1 parser
}

// Tokens for String Interpolation
const (
	tkRange = "<$RANGE$>"
	tkTS1   = "<$TS1$>"
	tkTS2   = "<$TS2$>"
)

// Common URL Parameter Names
const (
	ParamQuery  = "q"
	ParamDB     = "db"
	ParamFormat = "format"
)

// DefaultTimestampField is the default timestamp field name for v3 queries
const DefaultTimestampField = "time"

// ExtractQuery returns the SQL query text from a v3 request, decoding the
// POST body based on Content-Type. Supports GET (?q=), POST application/json
// ({"q":"..."}), POST application/x-www-form-urlencoded (q=...), and falls
// back to treating the raw POST body as SQL.
func ExtractQuery(r *http.Request) (string, error) {
	if !methods.HasBody(r.Method) {
		return r.URL.Query().Get(ParamQuery), nil
	}
	b, err := request.GetBody(r)
	if err != nil || len(b) == 0 {
		return "", err
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var payload struct {
			Q string `json:"q"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			return "", err
		}
		return payload.Q, nil
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals, err := url.ParseQuery(string(b))
		if err != nil {
			return "", err
		}
		return vals.Get(ParamQuery), nil
	}
	return string(b), nil
}

// EncodeBody wraps a SQL statement in the body format matching the request's
// Content-Type. Used to preserve the inbound body shape when Trickster
// rewrites the upstream request (e.g. on SetExtent).
func EncodeBody(r *http.Request, sqlQuery string) []byte {
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		b, _ := json.Marshal(map[string]string{ParamQuery: sqlQuery})
		return b
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		return []byte(url.Values{ParamQuery: {sqlQuery}}.Encode())
	}
	return []byte(sqlQuery)
}

var (
	lexOpts  = LexerOptions()
	lexer    = lsql.NewLexer(lexOpts)
	dfParser = &dfSQLParser{
		Parser: sqlparser.New(
			parsing.New(nil, lexer, lexOpts),
		).(*sqlparser.Parser),
	}
)

type dfSQLParser struct {
	*sqlparser.Parser
}

func parse(statement string) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	trq := &timeseries.TimeRangeQuery{Statement: statement}
	ro := &timeseries.RequestOptions{}
	rs, err := dfParser.Run(sqlparser.NewRunContext(trq, ro), dfParser, trq.Statement)
	results := rs.Results()
	verb, ok := rs.GetResultsCollection("verb")
	var canObjectCache bool
	if !ok {
		return nil, nil, false, sqlparser.ErrNotTimeRangeQuery
	}
	if vs, ok := verb.(string); ok {
		canObjectCache = vs == lsql.TokenValSelect
	}

	trq.CacheKeyElements = map[string]string{
		"query": trq.Statement,
	}

	returnWithKey := func(e error) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
		if canObjectCache {
			return trq, nil, canObjectCache, e
		}
		return nil, nil, canObjectCache, e
	}

	if err != nil {
		return returnWithKey(parsing.ParserError(err, rs.Current()))
	}
	var t *token.Token
	if psql.HasLimitClause(results) {
		return returnWithKey(pe.ErrNotTimeRangeQuery)
	}
	if t, err = parseGroupByTokens(results, trq); err != nil {
		return returnWithKey(parsing.ParserError(err, t))
	}
	if t, err = parseSelectTokens(results, trq, ro); err != nil {
		return returnWithKey(parsing.ParserError(err, t))
	}
	if t, err = parseWhereTokens(results, trq, ro); err != nil {
		return returnWithKey(parsing.ParserError(err, t))
	}
	return trq, ro, canObjectCache, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func ParseTimeRangeQuery(r *http.Request, f iofmt.Format,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	if r == nil || !f.IsV3SQL() {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}
	var qi url.Values
	isBody := methods.HasBody(r.Method)
	sqlQuery, err := ExtractQuery(r)
	if err != nil {
		return nil, nil, false, err
	}
	if !isBody {
		qi = r.URL.Query()
	}
	if sqlQuery == "" {
		return nil, nil, false, pe.MissingURLParam(ParamQuery)
	}

	trq, ro, canOPC, err := parse(sqlQuery)
	if err != nil {
		return trq, ro, canOPC, err
	}
	ro.OutputFormat = iofmt.V3OutputFormat(r)

	if isBody && trq != nil {
		trq.OriginalBody = []byte(sqlQuery)
	}
	if trq.BackfillTolerance == 0 {
		bf := time.Minute
		res := request.GetResources(r)
		if res != nil {
			bf = res.BackendOptions.BackfillTolerance
		}
		trq.BackfillTolerance = bf
	}
	trq.ParsedQuery = &Query{
		TokenizedStatement:     trq.Statement,
		BaseTimestampFieldName: ro.BaseTimestampFieldName,
	}
	trq.TemplateURL = urls.Clone(r.URL)
	if isBody {
		request.SetBody(r, EncodeBody(r, trq.Statement))
	} else {
		qi.Set(ParamQuery, trq.Statement)
		trq.TemplateURL.RawQuery = qi.Encode()
	}
	return trq, ro, canOPC, nil
}

// interval duration lookups for DataFusion INTERVAL literals and date_trunc
// unit strings. Units with variable length (month, quarter, year) are omitted
// — queries with those bucketings fall through to proxy.
var intervalDurations = map[string]time.Duration{
	"second":  time.Second,
	"seconds": time.Second,
	"minute":  time.Minute,
	"minutes": time.Minute,
	"hour":    time.Hour,
	"hours":   time.Hour,
	"day":     24 * time.Hour,
	"days":    24 * time.Hour,
	"week":    7 * 24 * time.Hour,
	"weeks":   7 * 24 * time.Hour,
}

func parseSelectTokens(results ts.Lookup,
	trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions,
) (*token.Token, error) {
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
	if len(st) == 0 {
		return nil, sqlparser.ErrMissingTimeseries
	}
	var foundTimeSeries bool
	for _, fieldParts := range st {
		if len(fieldParts) == 0 {
			continue
		}
		var isDateBin, isDateTrunc, expectAlias bool
		var intervalNum int
		var intervalUnit string
		for _, t := range fieldParts {
			if t.Typ == token.LeftParen || t.Typ == token.RightParen ||
				t.Typ == token.Space || t.Typ == lsql.TokenComment {
				continue
			}
			bucketing := isDateBin || isDateTrunc
			if bucketing {
				if t.Typ == lsql.TokenAs {
					expectAlias = true
					continue
				}
				if expectAlias {
					trq.TimestampDefinition.Name = t.Val
					break
				}
				if isDateBin {
					// INTERVAL keyword, ignored
					if t.Typ == tokenInterval {
						continue
					}
					if t.Typ == token.String {
						// e.g., '1 hour' — parse "N unit"
						parts := strings.Fields(lsql.UnQuote(t.Val))
						if len(parts) == 2 {
							n, err := strconv.Atoi(parts[0])
							if err == nil {
								intervalNum = n
								intervalUnit = strings.ToLower(parts[1])
							}
						}
						continue
					}
					if t.Typ == token.Number && intervalNum == 0 {
						n, err := strconv.Atoi(t.Val)
						if err == nil {
							intervalNum = n
						}
						continue
					}
					if t.Typ == token.Identifier && intervalNum > 0 && intervalUnit == "" {
						intervalUnit = strings.ToLower(t.Val)
						continue
					}
				}
				if isDateTrunc {
					// date_trunc('unit', time) — string is the unit, multiplier is 1
					if t.Typ == token.String && intervalUnit == "" {
						intervalUnit = strings.ToLower(lsql.UnQuote(t.Val))
						intervalNum = 1
						continue
					}
				}
				// the time column reference
				if t.Typ == token.Identifier && ro.BaseTimestampFieldName == "" {
					ro.BaseTimestampFieldName = t.Val
					continue
				}
			}
			if t.Typ == tokenDateBin {
				isDateBin = true
				foundTimeSeries = true
				continue
			}
			if t.Typ == tokenDateTrunc {
				isDateTrunc = true
				foundTimeSeries = true
				continue
			}
		}
		bucketing := isDateBin || isDateTrunc
		if bucketing && intervalNum > 0 && intervalUnit != "" {
			d, ok := intervalDurations[intervalUnit]
			if ok {
				trq.Step = d * time.Duration(intervalNum)
			}
		}
		if bucketing && !expectAlias {
			last := fieldParts[len(fieldParts)-1]
			trq.TimestampDefinition.Name = trq.Statement[fieldParts[0].Pos : last.Pos+len(last.Val)]
		}
	}
	if !foundTimeSeries {
		return nil, sqlparser.ErrMissingTimeseries
	}
	return nil, nil
}

func parseWhereTokens(results ts.Lookup,
	trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions,
) (*token.Token, error) {
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
	if l&1 != 1 {
		return nil, sqlparser.ErrTimerangeParse
	}
	var e timeseries.Extent
	var s1, e1, s2, e2 int
	var tsr1, tsr2 string
	var isBetween bool
	for n, fieldParts := range wt {
		var atLowerBound bool
		var state int
		var i int
		lfp := len(fieldParts)
		for i = 0; i < lfp; i++ {
			t := fieldParts[i]
			if t.Typ == token.LeftParen || t.Typ == token.RightParen ||
				t.Typ == token.Space || t.Typ == lsql.TokenComment {
				continue
			}
			if t.Typ.IsBreakable() {
				break
			}
		sw:
			switch state {
			case 0:
				if t.Val != ro.BaseTimestampFieldName && t.Val != trq.TimestampDefinition.Name {
					goto nextSet
				}
				state++
			case 1:
				isBetween = isBetween || t.Typ == lsql.TokenBetween
				if !isBetween && !t.Typ.IsGreaterOrLessThan() {
					return t, parsing.ErrUnexpectedToken
				}
				atLowerBound = isBetween || (t.Typ == token.GreaterThan ||
					t.Typ == token.GreaterThanOrEqual)
				state++
			case 2:
				ts, f, err := lsql.ParseTimeField(t)
				if err != nil {
					return t, err
				}
				trq.TimestampDefinition.ProviderData1 = byte(f)
				val, j, err := solveMathExpression(fieldParts[i:], ts)
				if err != nil {
					return t, err
				}
				t2 := t.Clone()
				t2.Val = strconv.FormatInt(val, 10)
				t2.Typ = token.Number
				if atLowerBound {
					e.Start, _, _ = lsql.TokenToTime(t2)
					tsr1 = t2.Val
					atLowerBound = false
				} else {
					e.End, _, _ = lsql.TokenToTime(t2)
					tsr2 = t2.Val
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
				if t.Typ == token.LogicalAnd {
					break sw
				}
				ts, _, err := lsql.ParseTimeField(t)
				if err != nil {
					return t, err
				}
				v, j, err := solveMathExpression(fieldParts[i:], ts)
				if err != nil {
					return t, err
				}
				e.End = time.Unix(v, 0)
				tsr2 = t.Val
				i += j
				state++
			}
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
		trq.Statement = strings.ReplaceAll(trq.Statement, r1, tkRange)
	}
	if r2 != "" {
		trq.Statement = strings.ReplaceAll(trq.Statement, r2, "")
	}
	if tsr1 != "" {
		trq.Statement = strings.ReplaceAll(trq.Statement, tsr1, tkTS1)
	}
	if tsr2 != "" {
		trq.Statement = strings.ReplaceAll(trq.Statement, tsr2, tkTS2)
	}
	return nil, nil
}

func parseGroupByTokens(results ts.Lookup,
	trq *timeseries.TimeRangeQuery,
) (*token.Token, error) {
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

// solveMathExpression handles simple integer math expressions like now() - 3600
func solveMathExpression(fieldParts token.Tokens, startValue int64,
) (int64, int, error) {
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
		if j%2 == 0 {
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
		if !t.Typ.IsAddOrSubtract() {
			return -1, i, parsing.ErrUnexpectedToken
		}
	nextIteration:
		prev = t
		j++
	}
	return v, i, nil
}
