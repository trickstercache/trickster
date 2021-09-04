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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// tokens for SELECT query
const (
	TokenValNull = "null"
	TokenValNaN  = "nan"
	// keyword tokens
	TokenValWith        = "with"
	TokenValSelect      = "select"
	TokenValFrom        = "from"
	TokenValJoin        = "join"
	TokenValAs          = "as"
	TokenValWhere       = "where"
	TokenValBetween     = "between"
	TokenValGroupBy     = "group by"
	TokenValOrderBy     = "order by"
	TokenValLimit       = "limit"
	TokenValUnion       = "union"
	TokenValHaving      = "having"
	TokenValIntoOutfile = "into outfile"
	TokenValAnd         = "and"
	TokenValOr          = "or"
	TokenValNot         = "not"
	TokenValAsc         = "asc"
	TokenValDesc        = "desc"
	// functions
	TokenValNow   = "now()"
	TokenValCount = "count"
)

// Comments
const (
	oneLinerComment = "--"
	leftComment     = "/*"
	rightComment    = "*/"
)

const (
	// TokenSQLNonKeyword is the lower bound value for SQL non-Keyword tokens, and must not be
	// assigned to a Token.Typ
	TokenSQLNonKeyword token.Typ = iota + (token.CustomNonKeyword + 128)
	// TokenComment is a Comment Token Type
	// comments are useful to Trickster, so we provide them as their own Typ to the parser
	TokenComment
	// TokenNull represents a token of "NULL"
	TokenNull
	// TokenNaN represents a token of "NaN"
	TokenNaN
	// TokenSQLNonKeywordExtensions is the marker after which custom extensions to this section
	// may start, and must not be assigned to a Token.Typ
	TokenSQLNonKeywordExtensions
	// TokenSQLNonKeywordEnd is the upper bound value for SQL Words that are not Keywords, and
	// must not be assigned to a Token.Typ
	TokenSQLNonKeywordEnd = TokenSQLNonKeyword + 256 // allow room for custom sql tokens
	// TokenSQLKeyword is the lower bound value for SQL Keywords and must not be assigned to a Token.Typ
	TokenSQLKeyword token.Typ = iota + (token.CustomKeyword + 128)
	// TokenSQLPrimaryKeyword is the lower bound value for Primary SQL Keywords and must not be
	// assigned to a Token.Typ
	TokenSQLPrimaryKeyword
	// TokenSQLVerb is the lower bound value for SQL Verbs and must not be assigned to a Token.Typ
	TokenSQLVerb
	// TokenWith represents a token of "WITH"
	TokenWith
	// TokenSelect represents a token of "SELECT"
	TokenSelect
	// TokenInsert represents a token of "INSERT"
	TokenInsert
	// TokenUpdate represents a token of "UPDATE"
	TokenUpdate
	// TokenDelete represents a token of "DELETE"
	TokenDelete
	// TokenTruncate represents a token of "TRUNCATE"
	TokenTruncate
	// TokenAlter represents a token of "ALTER"
	TokenAlter
	// TokenCreate represents a token of "CREATE"
	TokenCreate
	// TokenSQLVerbExtensions is the marker after which custom extensions to this section
	// may start, and must not be assigned to a Token.Typ
	TokenSQLVerbExtensions
	// TokenSQLVerbEnd is the upper bound value for SQL Verbs and must not be assigned to a Token.Typ
	TokenSQLVerbEnd = TokenSQLVerb + 128 // allow room for custom sql verbs
	// TokenSQLNonVerb is the lower bound value for SQL non-Verb primary keywords
	// and must not be assigned to a Token.Typ
	TokenSQLNonVerb = iota + TokenSQLVerbEnd + 1
	// TokenFrom represents a token of "FROM"
	TokenFrom
	// TokenJoin represents a token of "JOIN"
	TokenJoin
	// TokenWhere represents a token of "WHERE"
	TokenWhere
	// TokenBetween represents a token of "BETWEEN"
	TokenBetween
	// TokenGroupBy represents a token of "GROUP BY"
	TokenGroupBy
	// TokenHaving represents a token of "HAVING"
	TokenHaving
	// TokenOrderBy represents a token of "ORDER BY"
	TokenOrderBy
	// TokenUnion Represents a token of "UNION"
	TokenUnion
	// TokenLimit represents a token of "LIMIT"
	TokenLimit
	// TokenIntoOutfile represents a token of "INTO OUTFILE"
	TokenIntoOutfile
	// TokenSQLNonVerbExtensions is the marker after which custom extensions to this section
	// may start, and must not be assigned to a Token.Typ
	TokenSQLNonVerbExtensions
	// TokenSQLNonVerbEnd is the upper bound value for SQL Verbs and must not
	// be assigned to a Token.Typ
	TokenSQLNonVerbEnd = TokenSQLNonVerb + 256
	// TokenSQLPrimaryKeywordEnd is the upper bound value for Primary SQL Keywords
	// and must not be assigned to a Token.Typ
	TokenSQLPrimaryKeywordEnd = iota + TokenSQLNonVerbEnd + 1
	// TokenNowFunc represents a token of now() which we consider more of a keyword than a func
	TokenNowFunc
	// TokenAs represents a token of "AS"
	TokenAs
	// TokenAsc represents a token of "ASC"
	TokenAsc
	// TokenDesc represents a token of "DESC"
	TokenDesc
	// TokenSQLKeywordExtensions is the marker after which custom extensions to this section
	// may start, and must not be assigned to a Token.Typ
	TokenSQLKeywordExtensions
	// TokenSQLKeywordEnd is the end delimiter for SQL Keywords and must not be assigned to a Token.Typ
	TokenSQLKeywordEnd = TokenSQLPrimaryKeywordEnd + 1 + 128
	// TokenSQLFunction is the lower bound value for SQL Built-in Functions() and must not be assigned to a Token.Typ
	TokenSQLFunction token.Typ = TokenSQLKeywordEnd + 1
	// TokenCount represents the count() function
	TokenCount
	// TokenSQLFunctionExtensions is the marker after which custom extensions to this section
	// may start, and must not be assigned to a Token.Typ
	TokenSQLFunctionExtensions
	// TokenSQLFunctionEnd is the upper bound value for SQL Built-in Functions() and must not be assigned to a Token.Typ
	TokenSQLFunctionEnd = TokenSQLFunction + 512
)

// IsKeyword returns true if the token is a known SQL Keyword based on value range
func IsKeyword(t token.Typ) bool {
	return t > TokenSQLKeyword && t < TokenSQLKeywordEnd
}

// IsPrimaryKeyword returns true if the token is a known Primary SQL Keyword based on value range
func IsPrimaryKeyword(t token.Typ) bool {
	return t > TokenSQLPrimaryKeyword && t < TokenSQLPrimaryKeywordEnd
}

// IsNonVerbPrimaryKeyword returns true if the token is a known Primary SQL Keyword that
// is also not a Verb (SELECT, INSERT, etc) based on value range.
func IsNonVerbPrimaryKeyword(t token.Typ) bool {
	return t > TokenSQLNonVerb && t < TokenSQLNonVerbEnd
}

// IsVerb returns true if the token is a known SQL Verb based on value range
func IsVerb(t token.Typ) bool {
	return t > TokenSQLVerb && t < TokenSQLVerbEnd
}

// TokenToTime returns a time value derived from the token
func TokenToTime(i *token.Token) (time.Time, byte, error) {
	var t time.Time
	var err error
	switch i.Typ {
	case token.String:
		t, err = ParseBasicDateTime(UnQuote(i.Val))
		if err != nil {
			return t, 0, err
		}
	case token.Number:
		n, err := i.Int64()
		if err != nil {
			return t, 0, err
		}
		if n > 9999999999 {
			return time.Unix(n/1000, (n%1000)*1000000), 1, nil
		}
		return time.Unix(n, 0), 0, nil

	default:
		t = time.Now()
	}
	return t, 0, err
}
