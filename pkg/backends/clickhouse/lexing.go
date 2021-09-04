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
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

const (
	// clickhouse sql token types
	tokenClickHouseNonKeyword = iota + (lsql.TokenSQLNonKeyword + 30)
	tokenBy
	// ordered keywords, stitch into sql ordering
	// for more info, see lex/sql/lsql.go @ TokenSQLKeyword declaration
	tokenPreWhere   token.Typ = lsql.TokenWhere - 1
	tokenUnionAll   token.Typ = lsql.TokenLimit + 1
	tokenFormat     token.Typ = lsql.TokenIntoOutfile + 1
	tokenWithTotals token.Typ = lsql.TokenGroupBy + 1
	// Unordered keywords
	tokenIntDiv token.Typ = iota + (lsql.TokenSQLFunction + 10)
	tokenToInt32
	tokenToStartOfInterval
	tokenToStartOf
	tokenInterval
	tokenToDateFunc
	tokenWeek
	tokenDay
	tokenHour
	tokenMinute
	tokenSecond
)

// tokens for ClickHouse select
const (
	tokenValPreWhere   = "prewhere"
	tokenValFormat     = "format"
	tokenValUnionAll   = "union all"
	tokenValBy         = "by"
	tokenValWithTotals = "with totals"
	tokenValInterval   = "interval"
)

var chKey = map[string]token.Typ{
	"intdiv":                  tokenIntDiv,
	"toint32":                 tokenToInt32,
	"touint32":                tokenToInt32,
	"tomonday":                tokenToStartOf,
	"tostartofweek":           tokenToStartOf,
	"tostartofday":            tokenToStartOf,
	"tostartofhour":           tokenToStartOf,
	"tostartofminute":         tokenToStartOf,
	"tostartoffiveminute":     tokenToStartOf,
	"tostartoftenminutes":     tokenToStartOf,
	"tostartoffifteenminutes": tokenToStartOf,
	"tostartofinterval":       tokenToStartOfInterval,
	tokenValPreWhere:          tokenPreWhere,
	tokenValFormat:            tokenFormat,
	tokenValUnionAll:          tokenUnionAll,
	tokenValBy:                tokenBy,
	tokenValWithTotals:        tokenWithTotals,
	tokenValInterval:          tokenInterval,
	lsql.TokenValWith:         lsql.TokenWith,
	"week":                    tokenWeek,
	"day":                     tokenDay,
	"hour":                    tokenHour,
	"minute":                  tokenMinute,
	"second":                  tokenSecond,
	"todatetime":              tokenToDateFunc,
	"todate":                  tokenToDateFunc,
}

// LexerOptions returns a Clickhouse-crafted Lexer Options Pointer
func LexerOptions() *lex.Options {
	skw := lsql.SpacedKeywords()
	skw["union"] = 4 // union all
	skw["with"] = 7  // with totals
	return &lex.Options{
		CustomKeywords:     chKey,
		SpacedKeywordHints: skw,
	}
}
