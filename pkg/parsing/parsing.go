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

package parsing

import (
	"context"
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// Parser is the main parser interface tor Trickster
type Parser interface {
	// Run runs the parser.
	// context is a context to associate with the query for tracking request-specific info
	// that parser extension use to maintain separate state data from main parse (e.g.,
	// information about time series in the query). The parser argument is required so that
	// any structs extending the Parser (which may also be an extension) can infer the
	// original struct's type when running custom parser functions that require access
	// to private struct members. The string is the actual query/statement to parse
	Run(context.Context, Parser, string) (*RunState, error)
}

type parsingError struct {
	val string
	pos int
	typ token.Typ
	err error
}

// ParsingError represents a Parsing Error
var ParsingError error = &parsingError{}

func (p *parsingError) Error() string {
	return fmt.Sprintf("parser error='%s', position=%d, token='%s', type=%d",
		p.err, p.pos, p.val, p.typ,
	)
}

func (p *parsingError) Is(target error) bool {
	if target == nil {
		return false
	}
	_, ok := target.(*parsingError)
	return ok || p.err == target
}

// ParserError decorates an error with parser positioning information for troubleshooting context
func ParserError(err error, t *token.Token) error {
	if t == nil || err == nil {
		return err
	}
	return &parsingError{err: err, val: t.Val, pos: t.Pos, typ: t.Typ}
}

// StateFn is a state function that represents the processing of a group of similar tokens
type StateFn func(Parser, Parser, *RunState) StateFn

// DecisionSet is a map of token types to state func. DecisionSets are used to determine the
// next action when processing a token, based on the token (and possibly the next token's) type
type DecisionSet map[token.Typ]StateFn

// ErrEOF indicates EOF was reached during lexing or parsing
var ErrEOF = errors.New("eof")

// ErrNoLexer indicates that no lexer was specified to be used by the parser
var ErrNoLexer = errors.New("no lexer provided")

// ErrUnsupportedParser means the passed parser does not support the function being called
var ErrUnsupportedParser = errors.New("this state function is not supported by this parser")

//ErrUnexpectedToken is returned when the next token provided to the parser does not have a
// value in the current DecisionSet
var ErrUnexpectedToken = errors.New("unexpected token")

// ErrUnsupportedKeyword is returned when the RunState's current token is not a currently-supported
// keyword (e.g., UNION in a SQL query)
var ErrUnsupportedKeyword = errors.New("unsupported keyword")

// ErrInvalidKeywordOrder is returned when the RunState's current token a keyword that whose type value
// is lower than the previous keyword, which indicates they are presented out-of-order in the input string
var ErrInvalidKeywordOrder = errors.New("invalid keyword order")

// Noop is a convenience state function that breaks the main loop by returning a nil function
// Useful for assigning a nil-returning StateFn to a variable
func Noop(bp, ip Parser, rs *RunState) StateFn {
	return nil
}

// StateUnexpectedToken is a convenience state function that breaks the main loop by returning a nil function
// while attaching ErrUnexpectedToken to the RunState
func StateUnexpectedToken(bp, ip Parser, rs *RunState) StateFn {
	rs.WithError(ErrUnexpectedToken)
	return nil
}
