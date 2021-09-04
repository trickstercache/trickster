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
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing"
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/sqlparser"
)

const tqNoFrom = `WITH some value SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt`

const tq00 = `/* this tests a multi-line comment at the front, where the query continues after` +
	`, and on the same line as, the comment closing delimiter
  also, here we test: trickster-backfill-tolerance:30 */ WITH  'igor * 31 + \' dks( k )'  as  igor, 3600 as x ` +
	` SELECT (  intDiv(toUInt32(datetime), x) * x) * 1000 as t,` +
	` count() as cnt FROM test_db.test_table PREWHERE some_column = 'myvalue' WHERE datetime >= 1589904000 AND datetime < 1589997600` +
	` GROUP BY t, apple ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes // test comment
	// test 2 comment`

const tq01 = `SELECT toStartOfFiveMinute(datetime) AS t,` +
	` count() as cnt FROM test_db.test_table WHERE datetime > 1589904000` +
	` GROUP BY t ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes // test comment
	// test 2 comment`

const tq02 = `SELECT toStartOfInterval(datetime, INTERVAL 60 second) AS t,` +
	` count() as cnt FROM test_db.test_table WHERE datetime BETWEEN 1589904000 AND 1589997600` +
	` GROUP BY t, x ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes // test comment
	// test 2 comment`

const tq03 = `SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
	`FROM testdb.test_table WHERE time_column BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
	`AND date_column >= toDate(1516665600) AND date_column <= toDate(1516687200) ` +
	`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`
const tq04 = `SELECT toStartOfFiveMinute(datetime) AS t,` +
	` count() as cnt, testfield1, testfield2 FROM (SELECT * FROM test_db.test_table WHERE x = 1) WHERE datetime > 1589904000` +
	` GROUP BY t, testfield1, testfield2 ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes // test comment
	// test 2 comment`

const tq05 = `SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE datetime > ` +
	`'2020-01-01 00:00:00' AND datetime < now() - 300 GROUP BY t ORDER BY t FORMAT JSON`

const tq06 = `SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE datetime > ` +
	`'2020-01-01 00:00:00' AND datetime < now() - 300 GROUP BY t ORDER BY t FORMAT JSON`

const tq07 = `SELECT (  intDiv(toUInt32(datetime), 300) * 300) * 1000 AS t,` +
	` count() as cnt FROM test_db.test_table WHERE datetime between 1589904000 AND 1589997600` +
	` GROUP BY t ORDER BY  t DESC FORMAT JSON`

const tq08 = `SELECT (  intDiv(toUInt32(datetime), 300) * 300) * 1000 AS t,` +
	` count() as cnt FROM test_db.test_table WHERE datetime >= now() - 900` +
	` GROUP BY t ORDER BY  t DESC FORMAT JSON`

const bq00 = `SELECT toStartOfFiveMinute(datetime) AS t, l FROM table WHERE datetime BETWEEN 1 AND 5 LIMIT 10`

func TestParseRawQuery(t *testing.T) {
	tests := []struct {
		query string
		err   error
	}{
		{tq00, nil},
		{tq01, nil},
		{tq02, nil},
		{tq03, nil},
		{tq04, nil},
		{tq05, nil},
		{tq06, nil},
		{tq07, nil},
		{tq08, nil},
		{bq00, ErrLimitUnsupported},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, _, _, err := parse(test.query)
			if err != test.err {
				t.Errorf("got '%v' expected '%v'", err, test.err)
			}
		})
	}
}

func TestGoodQueries(t *testing.T) {
	trq, _, _, err := parse(tq07)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}

	if trq.Extent.Start != time.Unix(int64(1589904000), 0) {
		t.Errorf("Expected start time of 1589904000, got %d", trq.Extent.Start.Unix())
	}
	query := `SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE t > ` +
		`'2020-05-30 11:00:00' AND t < now() - 300 GROUP BY t, cnt FORMAT JSON`
	trq, _, _, err = parse(query)
	if err != nil {
		t.Error(err)
		return // otherwise, nil trq will panic below
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}

	query = `WITH dictGetString('test_cache', server, xxHash64(server)) as server_name ` +
		`SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt FROM test_db.test_table WHERE t > ` +
		`'2020-05-30 11:00:00' AND t < now() - 300  GROUP BY t, cnt FORMAT JSON`
	trq, _, _, err = parse(query)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}
	expected := `WITH dictGetString('test_cache', server, xxHash64(server)) as server_name ` +
		`SELECT toStartOfFiveMinute(datetime) AS t, count() as cnt ` +
		`FROM test_db.test_table WHERE <$RANGE$>   GROUP BY t, cnt FORMAT <$FORMAT$>`
	if trq.Statement != expected {
		t.Errorf("Tokenized statement did not match query:\n%s\n%s", trq.Statement, expected)
	}

	query = `SELECT toInt32(toStartOfFiveMinute(datetime)) AS t, count() as cnt FROM test_db.test_table WHERE datetime > ` +
		`'2020-05-30 11:00:00' AND datetime < now() - 300 GROUP BY t, cnt FORMAT JSON`
	trq, _, _, err = parse(query)
	if err != nil {
		t.Error(err)
	}
	if trq.Step != 300*time.Second {
		t.Errorf("Step of %d did not match 300 seconds", trq.Step)
	}
}

func TestParseErrors(t *testing.T) {
	_, _, _, err := parse("")
	if err != sqlparser.ErrNotTimeRangeQuery {
		t.Error("expecrted ErrNotTimeRangeQuery")
	}
}

func TestAtWith(t *testing.T) {

	rs := parsing.NewRunState(context.Background())
	ch := rs.Tokens()
	ch <- &token.Token{Typ: token.Space, Val: " "}
	rs.Next()
	f := atWith(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenWith, Val: "with"}
	rs.Next()
	atWith(nil, nil, rs)
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenWith, Val: "with"}
	ch <- &token.Token{Typ: lsql.TokenSelect, Val: "select"}
	rs.Next()
	f = atWith(parser, parser, rs)
	if f == nil {
		t.Error("expected non-nil StateFn")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenWith, Val: "with"}
	ch <- &token.Token{Typ: token.EOF}
	rs.Next()
	f = atWith(parser, parser, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenWith, Val: "with"}
	ch <- &token.Token{Typ: token.Identifier, Val: "x"}
	ch <- &token.Token{Typ: lsql.TokenSelect, Val: "select"}
	rs.Next()
	f = atWith(parser, parser, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != ErrInvalidWithClause {
		t.Error("expected ErrInvalidWithClause")
	}
}

func TestAtPreWhere(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := atPreWhere(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtFormat(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	ch := rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenComment}
	ch <- &token.Token{Typ: token.EOF}
	f := atFormat(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: token.Identifier, Val: "UnsupportedFormat"}
	ch <- &token.Token{Typ: token.EOF}
	f = atFormat(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != ErrUnsupportedOutputFormat {
		t.Error("expected ErrUnsupportedOutputFormat")
	}
}

func TestParseSelectTokens(t *testing.T) {
	_, err := parseSelectTokens(nil, nil, nil)
	if err != sqlparser.ErrMissingTimeseries {
		t.Error("expected ErrMissingTimeseries")
	}

	_, err = parseSelectTokens(map[string]interface{}{}, nil, nil)
	if err != sqlparser.ErrMissingTimeseries {
		t.Error("expected ErrMissingTimeseries")
	}

	_, err = parseSelectTokens(map[string]interface{}{"selectTokens": false}, nil, nil)
	if err != sqlparser.ErrMissingTimeseries {
		t.Error("expected ErrMissingTimeseries")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{},
	}, nil, nil)
	if err != sqlparser.ErrMissingTimeseries {
		t.Error("expected ErrMissingTimeseries")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{{}},
	}, nil, nil)
	if err != sqlparser.ErrMissingTimeseries {
		t.Error("expected ErrMissingTimeseries")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenToStartOfInterval},
				&token.Token{Typ: tokenInterval},
				&token.Token{Typ: token.Number, Val: "not-a-number"},
			},
		},
	}, nil, &timeseries.RequestOptions{})
	if err == nil {
		t.Error("expected invalid syntax error", err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenToStartOfInterval},
				&token.Token{Typ: tokenInterval},
				&token.Token{Typ: token.Number, Val: "97"},
				&token.Token{Typ: token.Identifier, Val: "year"},
			},
		},
	}, nil, &timeseries.RequestOptions{})
	if err != sqlparser.ErrStepParse {
		t.Error("expected step parser error", err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "1000"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err != nil {
		t.Error(err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "not-a-number"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "1000"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err == nil {
		t.Error("expected invalid syntax error", err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "not-a-number"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err == nil {
		t.Error("expected invalid syntax error", err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "360"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err != ErrUnsupportedOutputFormat {
		t.Error("expected ErrUnsupportedOutputFormat")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "not-a-number"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err == nil {
		t.Error("expected invalid syntax error", err)
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenIntDiv, Val: "intdiv"},
				&token.Token{Typ: tokenToInt32, Val: "touint32"},
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Comma, Val: ","},
				&token.Token{Typ: token.Number, Val: "120"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: token.Multiply, Val: "*"},
				&token.Token{Typ: token.Number, Val: "60"},
				&token.Token{Typ: lsql.TokenAs, Val: "as"},
				&token.Token{Typ: token.Identifier, Val: "y"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err != sqlparser.ErrStepParse {
		t.Error("expected ErrStepParse")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenToStartOf, Val: "invalid"},
			},
		},
	}, &timeseries.TimeRangeQuery{}, &timeseries.RequestOptions{})
	if err != ErrUnsupportedToStartOfFunc {
		t.Error("expected ErrUnsupportedToStartOfFunc")
	}

	_, err = parseSelectTokens(map[string]interface{}{
		"selectTokens": []token.Tokens{
			{
				&token.Token{Typ: tokenToStartOf, Val: "tostartoffiveminute"},
				&token.Token{Typ: token.Identifier, Val: "x"},
			},
		},
	}, &timeseries.TimeRangeQuery{Statement: "                                        "},
		&timeseries.RequestOptions{})
	if err != nil {
		t.Error(err)
	}

}

func TestParseTimeField(t *testing.T) {
	_, err := parseTimeField(&token.Token{Typ: token.Number, Val: "not-a-number"})
	if err == nil {
		t.Error("expected syntax error")
	}
}

func TestParseGroupByTokens(t *testing.T) {
	_, err := parseGroupByTokens(map[string]interface{}{
		"groupByTokens": nil,
	}, nil, nil)
	if err != lsql.ErrInvalidGroupByClause {
		t.Error("expected ErrInvalidGroupByClause")
	}
}

func TestGetInt(t *testing.T) {
	_, err := getInt(&token.Token{Typ: token.Number, Val: "not-a-number"})
	if err == nil {
		t.Error("expected parsing error")
	}
	_, err = getInt(&token.Token{Typ: token.Space, Val: "not-a-number"})
	if err != token.ErrParsingInt {
		t.Error("expected ErrParsingInt")
	}
}

func TestParseWhereTokens(t *testing.T) {
	rlo := &timeseries.RequestOptions{}
	tests := []struct {
		wt, pwt []token.Tokens
		wv      token.Lookup
		trq     *timeseries.TimeRangeQuery
		rlo     *timeseries.RequestOptions
		err     error
	}{
		{nil, nil, nil, nil, nil, nil},                                         // 0
		{nil, nil, nil, nil, rlo, sqlparser.ErrNotTimeRangeQuery},              // 1
		{[]token.Tokens{}, nil, nil, nil, rlo, sqlparser.ErrNotTimeRangeQuery}, // 2
		{[]token.Tokens{{&token.Token{Typ: token.EOF}}}, nil, nil, // 3
			nil, rlo, sqlparser.ErrNoLowerBound},
		{[]token.Tokens{{&token.Token{Typ: token.Identifier, Val: "x"}}}, nil, // 4
			token.Lookup{"x": &token.Token{Typ: token.Number, Val: "8480"}},
			&timeseries.TimeRangeQuery{}, rlo, sqlparser.ErrNoLowerBound},
		{[]token.Tokens{ // 5
			{
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.GreaterThan, Val: ">"},
				&token.Token{Typ: token.String, Val: "not-a-time"},
			},
		}, nil, nil, nil, &timeseries.RequestOptions{BaseTimestampFieldName: "x"},
			lsql.ErrInvalidInputLength,
		},
		{[]token.Tokens{ // 6
			{
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.GreaterThan, Val: "between"},
				&token.Token{Typ: token.Number, Val: "0"},
				&token.Token{Typ: token.LogicalAnd, Val: "and"},
				&token.Token{Typ: token.String, Val: "not-a-time"},
			},
		}, nil, nil, &timeseries.TimeRangeQuery{Step: 60 * time.Second},
			&timeseries.RequestOptions{BaseTimestampFieldName: "x"},
			lsql.ErrInvalidInputLength,
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			var m map[string]interface{}
			if test.wt != nil || test.pwt != nil || test.wv != nil {
				m = make(map[string]interface{})
				if len(test.wt) > 0 {
					m["whereTokens"] = test.wt
				}
				if len(test.pwt) > 0 {
					m["preWhereTokens"] = test.pwt
				}
				if len(test.wv) > 0 {
					m["withVars"] = test.wv
				}
			}
			_, err := parseWhereTokens(m, test.trq, test.rlo)
			if err != test.err {
				t.Errorf("got '%v' expected '%v'", err, test.err)
			}
		})
	}

	// cases the loop could not account for
	_, err := parseWhereTokens(map[string]interface{}{
		"whereTokens": false,
	},
		nil, &timeseries.RequestOptions{})
	if err != sqlparser.ErrNotTimeRangeQuery {
		t.Error("expected ErrNotTimeRangeQuery got", err)
	}
	_, err = parseWhereTokens(map[string]interface{}{
		"whereTokens": []token.Tokens{},
	},
		nil, &timeseries.RequestOptions{})
	if err != sqlparser.ErrTimerangeParse {
		t.Error("expected ErrTimerangeParse got", err)
	}
}

func TestSolveMathExpression(t *testing.T) {

	tests := []struct {
		input    token.Tokens
		withVars token.Lookup
		i1       int64
		i2       int
		err      error
	}{
		{
			token.Tokens{
				&token.Token{Typ: token.Identifier, Val: "x"},
				&token.Token{Typ: token.Plus, Val: "+"},
				&token.Token{Typ: token.Number, Val: "10"},
			},
			token.Lookup{
				"x": &token.Token{Typ: token.Number, Val: "5"},
			},
			15, 2, nil,
		},
		{
			token.Tokens{
				&token.Token{Typ: token.Number, Val: "not-a-number"},
				&token.Token{Typ: token.Plus, Val: "+"},
				&token.Token{Typ: token.Number, Val: "10"},
			},
			nil, -1, 0, strconv.ErrSyntax,
		},
		{
			token.Tokens{
				&token.Token{Typ: token.Number, Val: "5"},
				&token.Token{Typ: token.Multiply, Val: "*"}, // only add or subtract is supported
				&token.Token{Typ: token.Number, Val: "10"},
			},
			nil, -1, 1, parsing.ErrUnexpectedToken,
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			i1, i2, err := SolveMathExpression(test.input, 0, test.withVars)
			if i1 != test.i1 {
				t.Errorf("expected %d got %d", test.i1, i1)
			}
			if i2 != test.i2 {
				t.Errorf("expected %d got %d", test.i2, i2)
			}
			if test.err != nil && err == nil {
				t.Error("expected", test.err)
			} else if test.err == nil && err != nil {
				t.Error("expected no err, got", err)
			} else if !errors.Is(err, test.err) {
				t.Errorf("expected %s got %s", test.err, err)
			}
		})
	}
}

func TestBadQueries(t *testing.T) {
	tests := []struct {
		query string
		err   error
	}{
		{"SELECT too short", parsing.ParsingError},
		{`SELECT toStartOfInterval(datetime, INTERVAL a second) AS t,` +
			` count() as cnt FROM test_db.test_table WHERE datetime BETWEEN 1589904000 AND 1589997600` +
			` GROUP BY t, x ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes`, parsing.ParsingError},
		{`SELECT toStartOfInterval(datetime, INTERVAL 60 second) AS t,` +
			` count() as cnt FROM test_db.test_table WHERE datetime BETWEEN 1589904000 AND 1589997600` +
			` ORDER BY  t DESC FORMAT TabSeparatedWithNamesAndTypes`, sql.ErrInvalidGroupByClause},
		{`SELECT toStartOfMinute(datetime) as x, cnt FROM test_table` +
			` WHERE datetime between 0 and 10 GROUP BY x FORMAT invalid`, ErrUnsupportedOutputFormat},
		{`SELECT toStartOfMinute(datetime) as x, cnt FROM test_table` +
			` WHERE datetime between 10 GROUP BY x FORMAT json`, sqlparser.ErrNoUpperBound},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, _, _, err := parse(test.query)
			if test.err != nil && err == nil {
				t.Error("expected", test.err)
			} else if test.err == nil && err != nil {
				t.Error("expected no err, got", err)
			} else if !errors.Is(err, test.err) {
				t.Errorf("expected %s got %s", test.err, err)
			}
		})
	}

}

// func TestBackfillTolerance(t *testing.T) {
// 	var query string

// 	test := func(run string, bf int, query string, exp int) {
// 		t.Run(run, func(t *testing.T) {
// 			_, _, err := parse(query)
// 			if err != nil {
// 				t.Error(err)
// 			}
// 			actual := int(trq.BackfillTolerance.Seconds())
// 			if actual != exp {
// 				t.Errorf("Expected backfill tolerance of %d, got %d", exp, actual)
// 			}
// 		})
// 	}

// 	query = `select intDiv(toInt32(datetime), 20) * 20 * 1000 as t, sum(cnt) FROM test_table WHERE datetime >= '2020-06-01 11:00:00'` +
// 		" FORMAT JSON"
// 	test("Backfill from now should be at least configured value", 180, query, 180)
// 	query = `select intDiv(toInt32(datetime), 300) * 300 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
// 		` and datetime <= '2020-06-01 12:02:00' FORMAT JSON`
// 	test("Backfill from now bucket should be at least to prior bucket value", 60, query, 120)
// 	query = `select intDiv(toInt32(datetime), 20) * 20 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
// 		` and datetime <= '2020-06-01 12:01:00' FORMAT JSON`
// 	test("Backfill should be at least now - configured value value", 180, query, 120)
// 	query = `select intDiv(toInt32(datetime), 20) * 20 as t, sum(cnt) FROM testTable WHERE datetime >= '2020-06-01 11:00:00' ` +
// 		` and datetime <= '2020-06-01 11:50:00' FORMAT JSON`
// 	test("Backfill should be negative/ignored if too far back", 180, query, -540)
// }
