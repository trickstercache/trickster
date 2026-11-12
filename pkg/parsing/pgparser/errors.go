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

package pgparser

import "strings"

const (
	syntaxErrorMessage  = "syntax error"
	syntaxErrorPrefix   = syntaxErrorMessage + ": "
	unimplementedFormat = "unimplemented: %s"
	tokenEOF            = "EOF"
	spaceChars          = " \t\r\n\f"
)

// SyntaxError is a statement the parser could not read
type SyntaxError struct {
	// Message says what is wrong
	Message string
	// Near is the text of the token the parser stopped at
	Near string
	// Pos is the byte offset of that token in the statement
	Pos int
	// Lexical marks an error in the token itself, whose text is then the message
	Lexical bool
}

// Error implements the error interface
func (e *SyntaxError) Error() string {
	if e.Lexical {
		return "lexical error: " + e.Near
	}
	message := strings.TrimPrefix(e.Message, syntaxErrorPrefix)
	if message == syntaxErrorMessage {
		return "at or near \"" + e.Near + "\": " + syntaxErrorMessage
	}
	return "at or near \"" + e.Near + "\": " + syntaxErrorPrefix + message
}
