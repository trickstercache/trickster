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
	"strings"
	"time"
	"unicode"

	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// sqllexer holds the state of the scanner.
type sqllexer struct {
	Key                map[string]token.Typ // the map of keywords to Typs to use when lexing
	SpacedKeywordHints map[string]int
}

// NewLexer returns a new SQL Lexer reference
func NewLexer(lo *lex.Options) lex.Lexer {
	l := &sqllexer{
		Key: BaseKey(),
	}
	l.SpacedKeywordHints = SpacedKeywords()
	if lo != nil {
		for k, v := range lo.SpacedKeywordHints {
			l.SpacedKeywordHints[k] = v
		}
		for k, v := range lo.CustomKeywords {
			l.Key[k] = v
		}
	}
	return l
}

// BaseKey returns the base Key map for a SQL Lexer
func BaseKey() map[string]token.Typ {
	return map[string]token.Typ{
		TokenValSelect:      TokenSelect,
		TokenValFrom:        TokenFrom,
		TokenValJoin:        TokenJoin,
		TokenValWhere:       TokenWhere,
		TokenValBetween:     TokenBetween,
		TokenValGroupBy:     TokenGroupBy,
		TokenValOrderBy:     TokenOrderBy,
		TokenValLimit:       TokenLimit,
		TokenValHaving:      TokenHaving,
		TokenValUnion:       TokenUnion,
		TokenValIntoOutfile: TokenIntoOutfile,
		TokenValNull:        TokenNull,
		TokenValNaN:         TokenNaN,
		TokenValAs:          TokenAs,
		TokenValAnd:         token.LogicalAnd,
		TokenValOr:          token.LogicalOr,
		TokenValNow:         TokenNowFunc,
		TokenValNot:         token.NotEqualOperator,
		TokenValCount:       TokenCount,
		TokenValAsc:         TokenAsc,
		TokenValDesc:        TokenDesc,
	}
}

// Run runs the lexer against the provided string and returns tokens on the
// provided channel. Run will end when the state is EOF or Error.
func (l *sqllexer) Run(input string, ch chan *token.Token) {
	rc := &lex.RunState{
		Input:        input,
		InputLowered: strings.ToLower(input),
		InputWidth:   len(input),
		Tokens:       ch,
	}
	for state := lexText; state != nil; {
		state = state(l, rc)
	}
	close(ch)
}

// state functions

// lexEOLComment scans a // comment that terminates at the end of the line
// it assumes you have already identified '//' and are positioned on the first slash
func lexEOLComment(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Pos += 2
	i := strings.Index(rs.InputLowered[rs.Pos:], "\n")
	if i == -1 {
		rs.Pos = rs.InputWidth
		rs.Emit(TokenComment)
		return lexText
	}
	i += rs.Pos
	if i > 0 && rs.InputLowered[i-1] == '\r' {
		i--
	}
	rs.Pos = i
	rs.Emit(TokenComment)
	return lexText
}

// lexComment scans a comment. The left comment marker is known to be present.
func lexComment(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Pos += len(leftComment)
	i := strings.Index(rs.InputLowered[rs.Pos:], rightComment)
	if i < 0 {
		rs.EmitToken(rs.Errorf("unclosed comment"))
		return nil
	}
	rs.Pos += (i + 2)
	rs.Emit(TokenComment)
	return lexText
}

var lexTextDecisions map[rune]lex.StateFn

func init() {
	lexTextDecisions = map[rune]lex.StateFn{
		lex.EOF:             emitEOF,
		lex.RuneSlash:       handleSlash,
		lex.RuneSpace:       lexSpace,
		lex.RuneTab:         lexSpace,
		lex.RuneCR:          lexNewline,
		lex.RuneLF:          lexNewline,
		lex.RuneAsterisk:    emitAsterisk,
		lex.RuneExclamation: handleExclamation,
		lex.RuneGreaterThan: handleGreaterThan,
		lex.RuneLessThan:    handleLessThan,
		lex.RuneEqual:       emitEqual,
		lex.RuneSingleQuote: lexSingleQuote,
		lex.RuneLeftParen:   handleLeftParen,
		lex.RuneRightParen:  handleRightParen,
		lex.RuneComma:       emitComma,
		lex.RunePlus:        emitPlus,
		lex.RuneMinus:       emitMinus,
		lex.RuneZero:        handleNumber,
		lex.RuneOne:         handleNumber,
		lex.RuneTwo:         handleNumber,
		lex.RuneThree:       handleNumber,
		lex.RuneFour:        handleNumber,
		lex.RuneFive:        handleNumber,
		lex.RuneSix:         handleNumber,
		lex.RuneSeven:       handleNumber,
		lex.RuneEight:       handleNumber,
		lex.RuneNine:        handleNumber,
		lex.RuneA:           handleIdentifier,
		lex.RuneB:           handleIdentifier,
		lex.RuneC:           handleIdentifier,
		lex.RuneD:           handleIdentifier,
		lex.RuneE:           handleIdentifier,
		lex.RuneF:           handleIdentifier,
		lex.RuneG:           handleIdentifier,
		lex.RuneH:           handleIdentifier,
		lex.RuneI:           handleIdentifier,
		lex.RuneJ:           handleIdentifier,
		lex.RuneK:           handleIdentifier,
		lex.RuneL:           handleIdentifier,
		lex.RuneM:           handleIdentifier,
		lex.RuneN:           handleIdentifier,
		lex.RuneO:           handleIdentifier,
		lex.RuneP:           handleIdentifier,
		lex.RuneQ:           handleIdentifier,
		lex.RuneR:           handleIdentifier,
		lex.RuneS:           handleIdentifier,
		lex.RuneT:           handleIdentifier,
		lex.RuneU:           handleIdentifier,
		lex.RuneV:           handleIdentifier,
		lex.RuneW:           handleIdentifier,
		lex.RuneX:           handleIdentifier,
		lex.RuneY:           handleIdentifier,
		lex.RuneZ:           handleIdentifier,
	}
}

func emitEOF(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.EOF)
	return nil
}

func emitComma(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.Comma)
	return lexText
}

func emitAsterisk(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.Multiply)
	return lexText
}

func emitEqual(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.Equals)
	return lexText
}

func emitPlus(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.Plus)
	return lexText
}

func emitMinus(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.Minus)
	return lexText
}

func handleGreaterThan(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	t := token.GreaterThan
	if rs.Peek() == lex.RuneEqual {
		rs.Next()
		t = token.GreaterThanOrEqual
	}
	rs.Emit(t)
	return lexText
}

func handleLessThan(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	t := token.LessThan
	if rs.Peek() == lex.RuneEqual {
		rs.Next()
		t = token.LessThanOrEqual
	}
	rs.Emit(t)
	return lexText
}

func handleSlash(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	l := li.(*sqllexer)
	if rs.Peek() == lex.RuneSlash {
		return lexEOLComment(l, rs)
	}
	if rs.Peek() == lex.RuneAsterisk {
		return lexComment(l, rs)
	}
	rs.Emit(token.Divide)
	return lexText
}

func handleExclamation(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	if rs.Peek() == lex.RuneEqual {
		rs.Next()
	}
	rs.Emit(token.InequalityOperator)
	return lexText
}

func handleNumber(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Backup()
	return lexNumber
}

func handleIdentifier(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Backup()
	return lexIdentifier
}

func handleLeftParen(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.LeftParen)
	rs.ParenDepth++
	return lexText
}

func handleRightParen(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	rs.Emit(token.RightParen)
	rs.ParenDepth--
	if rs.ParenDepth < 0 {
		return rs.EmitToken(rs.Errorf("unexpected right paren"))
	}
	return lexText
}

// lexText scans for keywords or other lexable elements in the main string
func lexText(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	// Either number, quoted string, or identifier.
	// Spaces separate arguments; runs of spaces turn into TokenSpace.
	// Pipe symbols separate and are emitted.
	r := rs.Next()
	if f, ok := lexTextDecisions[r]; ok {
		return f(li, rs)
	}
	if r <= unicode.MaxASCII && unicode.IsPrint(r) {
		rs.Emit(token.Char)
		return lexText
	}
	return rs.EmitToken(rs.Errorf("unrecognized character in action: %#U", r))
}

// lexNewline scans a run of newline characters.
// We have not consumed the first space, which is known to be present.
// Take care if there is a trim-marked right delimiter, which starts with a space.
func lexNewline(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	var r rune
	for {
		r = rs.Peek()
		if !lex.IsWhiteSpace(r) {
			break
		}
		rs.Next()
	}
	rs.Emit(token.Space)
	return lexText
}

// lexSpace scans a run of space characters.
// We have not consumed the first space, which is known to be present.
// Take care if there is a trim-marked right delimiter, which starts with a space.
func lexSpace(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	var r rune
	for {
		r = rs.Peek()
		if !lex.IsSpace(r) {
			break
		}
		rs.Next()
	}
	rs.Emit(token.Space)
	return lexText
}

// SpacedKeywords gives hints to lexIdentifier to check for keywords that have a space in
// them. the integer is length of the real keyword, minus the the key's length
var SpacedKeywords = func() map[string]int {
	return map[string]int{
		"group": 3, // group by
		"order": 3, // order by
		"into":  8, // into outfile
		"now":   2, // now()
	}
}

// lexIdentifier scans an alphanumeric.
func lexIdentifier(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	l := li.(*sqllexer)
Loop:
	for {
		switch r := rs.Next(); {
		case lex.IsAlphaNumeric(r), r == lex.RuneUnderscore || r == lex.RunePeriod:
		default:
			rs.Backup()
			word := rs.InputLowered[rs.Start:rs.Pos]
			if !rs.AtTerminator() {
				return rs.EmitToken(rs.Errorf("bad character %#U", r))
			}
			// if we have a word like group, order, union, into, etc., where it
			// might be the first of a spaced keyword like "group by", this section
			// looks ahead however far the first word prescribes to verify the true match,
			// then adjusts the lexer pos to allow the TokenKeyword case to match
			if pad, ok := l.SpacedKeywordHints[word]; ok && rs.Pos+pad < rs.InputWidth &&
				l.Key[rs.InputLowered[rs.Start:rs.Pos+pad]] > TokenSQLKeyword &&
				l.Key[rs.InputLowered[rs.Start:rs.Pos+pad]] < TokenSQLFunction {
				word = rs.InputLowered[rs.Start : rs.Pos+pad]
				rs.Pos += pad
			}
			t := l.Key[word]
			switch {
			case t > TokenSQLKeyword, token.IsLogicalOperator(t):
				rs.Emit(t)
			case word == token.TokenValTrue, word == token.TokenValFalse:
				rs.Emit(token.Bool)
			default:
				rs.Emit(token.Identifier)
			}
			break Loop
		}
	}
	return lexText
}

// sourced from https://golang.org/src/text/template/parse/lex.go
// lexNumber scans a number: decimal, octal, hex, float, or imaginary. This
// isn't a perfect number scanner - for instance it accepts "." and "0x0.2"
// and "089" - but when it's wrong the input is invalid and the parser (via
// strconv) will notice.
func lexNumber(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	if !rs.ScanNumber() {
		return rs.EmitToken(rs.Errorf("bad number syntax: %q", rs.InputLowered[rs.Start:rs.Pos]))
	}
	if sign := rs.Peek(); sign == '+' || sign == '-' {
		// Complex: 1+2i. No spaces, must end in 'i'.
		if !rs.ScanNumber() || rs.InputLowered[rs.Pos-1] != 'i' {
			return rs.EmitToken(rs.Errorf("bad number syntax: %q", rs.InputLowered[rs.Start:rs.Pos]))
		}
		rs.Emit(token.Complex)
	} else {
		rs.Emit(token.Number)
	}
	return lexText
}

// lexSingleQuote scans a quoted string.
func lexSingleQuote(li lex.Lexer, rs *lex.RunState) lex.StateFn {
	var r rune
Loop:
	for {
		r = rs.Next()
		switch r {
		case '\\':
			if r = rs.Peek(); r == '\'' {
				rs.Next()
			}
		case lex.EOF, '\n':
			return rs.EmitToken(rs.Errorf("unterminated quoted string"))
		case '\'':
			if r = rs.Peek(); r == '\'' {
				rs.Next()
			} else {
				break Loop
			}
		}
	}
	rs.Emit(token.String)
	return lexText
}

// BasicDateFormat is the go-formatted date representation of a SQL Basic Date
const BasicDateFormat = "2006-01-02 15:04:05"

// ParseBasicDateTime parses a basic sql date time in the format of
// YYYY-MM-DD HH:MM:SS
func ParseBasicDateTime(input string) (time.Time, error) {
	if len(input) != 19 {
		return time.Time{}, ErrInvalidInputLength
	}
	return time.Parse(BasicDateFormat, input)
}

// UnQuote removes the single quotes surrounding a string, but only if both are present
func UnQuote(input string) string {
	if len(input) < 3 {
		if input == "''" {
			return ""
		}
		return input
	}
	if input[0] == '\'' && input[len(input)-1] == '\'' {
		return input[1 : len(input)-1]
	}
	return input
}
