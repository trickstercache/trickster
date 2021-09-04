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

package lex

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// RunState contains all the information about a particular run
type RunState struct {
	Input        string            // the string being scanned
	InputLowered string            // the lowercase version of the string being scanned
	InputWidth   int               // width of the input (so we don't have to keep calling len)
	Pos          int               // current position in the input
	Start        int               // start position of this Token
	Width        int               // width of last rune read from input
	Tokens       chan *token.Token // channel of scanned items
	ParenDepth   int               // nesting depth of ( ) exprs
}

// Next returns the next rune in the input.
func (rs *RunState) Next() rune {
	if int(rs.Pos) >= rs.InputWidth {
		rs.Width = 0
		return EOF
	}
	r, w := utf8.DecodeRuneInString(rs.InputLowered[rs.Pos:])
	rs.Width = w
	rs.Pos += rs.Width
	return r
}

// Peek returns but does not consume the next rune in the input.
func (rs *RunState) Peek() rune {
	r := rs.Next()
	rs.Backup()
	return r
}

// Backup steps back one rune. Can only be called once per call of next.
func (rs *RunState) Backup() {
	rs.Pos -= rs.Width
}

// Emit passes an Token back to the client.
func (rs *RunState) Emit(t token.Typ) {
	rs.Tokens <- &token.Token{Typ: t, Pos: rs.Start, Val: rs.InputLowered[rs.Start:rs.Pos]}
	rs.Start = rs.Pos
}

// EmitToken passes a pre-built an Token back to the client. Returning nil allows the
// function output to be used as a value when needd to consolidate EmitToken and the
// likely subsequent return nil
func (rs *RunState) EmitToken(i *token.Token) StateFn {
	rs.Tokens <- i
	rs.Start = rs.Pos
	return nil
}

// Ignore skips over the pending input before this point.
func (rs *RunState) Ignore() {
	rs.Start = rs.Pos
}

// Accept consumes the next rune if it's from the valid set.
func (rs *RunState) Accept(valid string) bool {
	if strings.ContainsRune(valid, rs.Next()) {
		return true
	}
	rs.Backup()
	return false
}

// AcceptRun consumes a run of runes from the valid set.
func (rs *RunState) AcceptRun(valid string) bool {
	var b bool
	for strings.ContainsRune(valid, rs.Next()) {
		b = true
	}
	rs.Backup()
	return b
}

// // Drain drains the output so the lexing goroutine will exit.
// // Called by the parser, not in the lexing goroutine.
// func (rs *RunState) Drain() {
// 	for range rs.Tokens {
// 	}
// }

// ScanNumber returns true if pending input is a number
func (rs *RunState) ScanNumber() bool {
	// Optional leading sign.
	rs.Accept("+-")
	// Is it hex?
	digits := "0123456789_"
	if rs.Accept("0") {
		// Note: Leading 0 does not mean octal in floats.
		if rs.Accept("xX") {
			digits = "0123456789abcdefABCDEF_"
		} else if rs.Accept("oO") {
			digits = "01234567_"
		} else if rs.Accept("bB") {
			digits = "01_"
		}
	}
	rs.AcceptRun(digits)
	if rs.Accept(".") {
		rs.AcceptRun(digits)
	}
	if len(digits) == 10+1 && rs.Accept("eE") {
		rs.Accept("+-")
		rs.AcceptRun("0123456789_")
	}
	if len(digits) == 16+6+1 && rs.Accept("pP") {
		rs.Accept("+-")
		rs.AcceptRun("0123456789_")
	}
	// Is it imaginary?
	rs.Accept("i")
	// Next thing mustn't be alphanumeric.
	if IsAlphaNumeric(rs.Peek()) {
		rs.Next()
		return false
	}
	return true
}

var terminators = map[rune]interface{}{
	' ':  nil,
	'\t': nil,
	'\n': nil,
	'\r': nil,
	EOF:  nil,
	',':  nil,
	';':  nil,
	')':  nil,
	'(':  nil,
}

// AtTerminator reports whether the input is at valid termination character to
// appear after an identifier. Breaks .X.Y into two pieces. Also catches cases
// like "$x+2" not being acceptable without a space, in case we decide one
// day to implement arithmetic.
func (rs *RunState) AtTerminator() bool {
	r := rs.Peek()
	_, ok := terminators[r]
	return ok
}

// Errorf returns an error token and terminates the scan by passing
// back a nil pointer that will be the next state, terminating l.NextToken.
func (rs *RunState) Errorf(format string, args ...interface{}) *token.Token {
	return &token.Token{Typ: token.Error, Pos: rs.Start, Val: fmt.Sprintf(format, args...)}
}
