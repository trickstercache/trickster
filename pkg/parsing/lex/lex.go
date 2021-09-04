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
	"unicode"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// EOF represents EOF as a value of -1
const EOF = -1

// Lexer is the Lexer interface
type Lexer interface {
	Run(string, chan *token.Token)
}

// Options provides members that alter the behavior of the underlying Lexer
type Options struct {
	CustomKeywords     map[string]token.Typ
	SpacedKeywordHints map[string]int
}

// StateFn represents the state of the scanner as a function that returns the next state.
type StateFn func(Lexer, *RunState) StateFn

// SpaceCharLookup is a map of acceptable space characters
var SpaceCharLookup = map[byte]interface{}{
	9:  nil, // "\t"
	10: nil, // "\n"
	13: nil, // "\r"
	32: nil, // " "
}

// IsWhiteSpace reports whether r is a whitespace character.
func IsWhiteSpace(r rune) bool {
	_, ok := SpaceCharLookup[byte(r)]
	return ok
}

// BaseLexer provides the parts needed to run the basic lexing framework
type BaseLexer struct {
	Key map[string]token.Typ // the map of keywords to Typs to use when lexing
}

// IsSpace reports whether r is a space character.
func IsSpace(r rune) bool {
	return r == ' ' || r == '\t'
}

// IsEndOfLine reports whether r is an end-of-line character.
func IsEndOfLine(r rune) bool {
	return r == '\r' || r == '\n'
}

// IsAlphaNumeric reports whether r is an alphabetic, digit.
func IsAlphaNumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
