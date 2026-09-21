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

// Package sqlscan is a lexical scanner for PostgreSQL-flavored SQL. It finds
// token boundaries without parsing, so callers can classify a statement or
// locate a clause while never mistaking quoted or commented text for SQL.
package sqlscan

import "strings"

// Kind classifies a token.
type Kind uint8

const (
	// Word is an unquoted identifier or keyword.
	Word Kind = iota + 1
	// QuotedIdent is a double-quoted identifier, including a U& form.
	QuotedIdent
	// String is any string constant: plain, E, U&, B, X, or dollar-quoted.
	String
	// Number is a numeric constant.
	Number
	// Param is a positional parameter such as $1.
	Param
	// Punct is a single punctuation or operator character.
	Punct
)

// Token is one lexical element; Start and End are byte offsets into the source.
type Token struct {
	Kind  Kind
	Start int
	End   int
	// Depth is the parenthesis nesting level at the token; an opening
	// parenthesis reports the level outside it.
	Depth int
}

// Options adjusts scanning for session settings that change the lexical rules.
type Options struct {
	// BackslashEscapes treats a backslash in a plain string constant as an
	// escape, as PostgreSQL does when standard_conforming_strings is off.
	BackslashEscapes bool
}

// Scanner yields the tokens of one SQL source, skipping whitespace and comments.
// It never fails: an unterminated construct runs to the end and sets Unterminated.
type Scanner struct {
	src   string
	pos   int
	depth int
	opts  Options
	// Unterminated reports that a string, identifier or comment never closed.
	Unterminated bool
}

// New returns a Scanner over src.
func New(src string, opts Options) *Scanner {
	return &Scanner{src: src, opts: opts}
}

// Text returns the source text of a token.
func (s *Scanner) Text(t Token) string { return s.src[t.Start:t.End] }

// Next returns the next token, or false at the end of the source.
func (s *Scanner) Next() (Token, bool) {
	s.skipSpaceAndComments()
	if s.pos >= len(s.src) {
		return Token{}, false
	}
	start, c := s.pos, s.src[s.pos]
	token := Token{Start: start, Depth: s.depth}
	switch {
	case c == '\'':
		s.scanQuoted('\'', s.opts.BackslashEscapes)
		token.Kind = String
	case c == '"':
		s.scanQuoted('"', false)
		token.Kind = QuotedIdent
	case c == '$':
		token.Kind = s.scanDollar()
	case isDigit(c) || c == '.' && s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1]):
		s.scanNumber()
		token.Kind = Number
	case isIdentStart(c):
		token.Kind = s.scanWordOrPrefixedString()
	default:
		s.pos++
		token.Kind = Punct
		switch c {
		case '(':
			s.depth++
		case ')':
			if s.depth > 0 {
				s.depth--
			}
			token.Depth = s.depth
		}
	}
	token.End = s.pos
	return token, true
}

func (s *Scanner) skipSpaceAndComments() {
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			s.pos++
		case c == '-' && strings.HasPrefix(s.src[s.pos:], "--"):
			if end := strings.IndexByte(s.src[s.pos:], '\n'); end >= 0 {
				s.pos += end + 1
			} else {
				s.pos = len(s.src)
			}
		case c == '/' && strings.HasPrefix(s.src[s.pos:], "/*"):
			s.skipBlockComment()
		default:
			return
		}
	}
}

func (s *Scanner) skipBlockComment() {
	// PostgreSQL block comments nest, unlike those of most other dialects.
	nesting := 0
	for s.pos < len(s.src) {
		switch {
		case strings.HasPrefix(s.src[s.pos:], "/*"):
			nesting++
			s.pos += 2
		case strings.HasPrefix(s.src[s.pos:], "*/"):
			nesting--
			s.pos += 2
			if nesting == 0 {
				return
			}
		default:
			s.pos++
		}
	}
	s.Unterminated = true
}

func (s *Scanner) scanQuoted(quote byte, backslashEscapes bool) {
	// consumes a constant delimited by quote, where a doubled quote is
	// a literal one and, optionally, a backslash escapes the next character.
	s.pos++
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case backslashEscapes && c == '\\' && s.pos+1 < len(s.src):
			s.pos += 2
		case c == quote:
			s.pos++
			if s.pos >= len(s.src) || s.src[s.pos] != quote {
				return
			}
			s.pos++
		default:
			s.pos++
		}
	}
	s.Unterminated = true
}

func (s *Scanner) scanDollar() Kind {
	rest := s.src[s.pos+1:]
	if rest != "" && isDigit(rest[0]) {
		s.pos++
		for s.pos < len(s.src) && isDigit(s.src[s.pos]) {
			s.pos++
		}
		return Param
	}
	// A dollar-quote tag is an identifier with no dollar sign, or is empty.
	tagLen := 0
	for tagLen < len(rest) && isIdentPart(rest[tagLen]) && rest[tagLen] != '$' {
		tagLen++
	}
	if tagLen >= len(rest) || rest[tagLen] != '$' || tagLen > 0 && isDigit(rest[0]) {
		s.pos++
		return Punct
	}
	delimiter := s.src[s.pos : s.pos+tagLen+2]
	s.pos += len(delimiter)
	if end := strings.Index(s.src[s.pos:], delimiter); end >= 0 {
		s.pos += end + len(delimiter)
	} else {
		s.pos = len(s.src)
		s.Unterminated = true
	}
	return String
}

func (s *Scanner) scanNumber() {
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		exponentSign := (c == '+' || c == '-') && (s.src[s.pos-1] == 'e' || s.src[s.pos-1] == 'E')
		if !isDigit(c) && c != '.' && c != '_' && !isIdentStart(c) && !exponentSign {
			return
		}
		s.pos++
	}
}

func (s *Scanner) scanWordOrPrefixedString() Kind {
	// consumes a word, or the string or identifier it
	// prefixes: E'..', B'..', X'..', N'..', U&'..' and U&"..".
	start := s.pos
	for s.pos < len(s.src) && isIdentPart(s.src[s.pos]) {
		s.pos++
	}
	if s.pos >= len(s.src) {
		return Word
	}
	word, next := s.src[start:s.pos], s.src[s.pos]
	if len(word) == 1 && next == '\'' {
		switch word[0] {
		case 'e', 'E':
			s.scanQuoted('\'', true)
			return String
		case 'b', 'B', 'x', 'X', 'n', 'N':
			s.scanQuoted('\'', s.opts.BackslashEscapes)
			return String
		}
	}
	if len(word) == 1 && (word[0] == 'u' || word[0] == 'U') && next == '&' && s.pos+1 < len(s.src) {
		switch s.src[s.pos+1] {
		case '\'':
			s.pos++
			s.scanQuoted('\'', false)
			return String
		case '"':
			s.pos++
			s.scanQuoted('"', false)
			return QuotedIdent
		}
	}
	return Word
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	// Bytes above ASCII are identifier characters in PostgreSQL.
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

// IsWord reports whether a token is the unquoted word, compared without case.
func (s *Scanner) IsWord(t Token, word string) bool {
	return t.Kind == Word && strings.EqualFold(s.src[t.Start:t.End], word)
}
