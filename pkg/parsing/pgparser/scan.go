// Copyright 2015 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// Modified by The Trickster Authors in 2026: literal handling simplified and LIKE operator spellings added.

package pgparser

import (
	"fmt"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/lex"
)

const (
	eof                  = -1
	errUnterminated      = "unterminated string"
	errInvalidUTF8       = "invalid UTF-8 byte sequence"
	errInvalidHexNumeric = "invalid hexadecimal numeric literal"
	singleQuote          = '\''
	identQuote           = '"'
	backtickQuote        = '`'
)

// Scanner lexes SQL statements.
type scanner struct {
	in            string
	pos           int
	bytesPrealloc []byte

	durations bool
	backticks bool

	// lastAttemptedID indicates the ID of the last attempted
	// token. Used to recognizd which token an error was encountered
	// on.
	lastAttemptedID int32
	// quoted indicates if the last identifier scanned was
	// quoted. Used to distinguish between quoted and non-quoted in
	// Inspect.
	quoted bool
}

// Init initializes a new Scanner that will process str.
func (s *scanner) init(str string) {
	s.in = str
	s.pos = 0
	// Preallocate some buffer space for identifiers etc.
	s.bytesPrealloc = make([]byte, len(str))
}

func (s *scanner) allocBytes(length int) []byte {
	if cap(s.bytesPrealloc) >= length {
		res := s.bytesPrealloc[:length:length]
		s.bytesPrealloc = s.bytesPrealloc[length:cap(s.bytesPrealloc)]
		return res
	}
	return make([]byte, length)
}

// buffer returns an empty []byte buffer that can be appended to. Any unused
// portion can be returned later using returnBuffer.
func (s *scanner) buffer() []byte {
	buf := s.bytesPrealloc[:0]
	s.bytesPrealloc = nil
	return buf
}

// returnBuffer returns the unused portion of buf to the Scanner, to be used for
// future allocBytes() or buffer() calls. The caller must not use buf again.
func (s *scanner) returnBuffer(buf []byte) {
	if len(buf) < cap(buf) {
		s.bytesPrealloc = buf[len(buf):]
	}
}

// finishString casts the given buffer to a string and returns the unused
// portion of the buffer. The caller must not use buf again.
func (s *scanner) finishString(buf []byte) string {
	str := *(*string)(unsafe.Pointer(&buf))
	s.returnBuffer(buf)
	return str
}

func (s *scanner) scanSetup(lval *sqlSymType) (int, bool) {
	lval.SetID(0)
	lval.SetPos(int32(s.pos)) // #nosec G115 -- ParseQuery rejects statements of 2 GiB or more
	lval.SetStr("EOF")
	s.quoted = false
	s.lastAttemptedID = 0

	if _, ok := s.skipWhitespace(lval, true); !ok {
		return 0, true
	}

	ch := s.next()
	if ch == eof {
		lval.SetPos(int32(s.pos)) // #nosec G115 -- ParseQuery rejects statements of 2 GiB or more
		return ch, false
	}

	lval.SetID(int32(ch))         // #nosec G115 -- the value is one single-byte character
	lval.SetPos(int32(s.pos - 1)) // #nosec G115 -- ParseQuery rejects statements of 2 GiB or more
	lval.SetStr(s.in[lval.Pos():s.pos])
	s.lastAttemptedID = int32(ch) // #nosec G115 -- the value is one single-byte character
	return ch, false
}

// Scan scans the next token and populates its information into lval.
func (s *scanner) scan(lval *sqlSymType) {
	ch, skipWhiteSpace := s.scanSetup(lval)

	if skipWhiteSpace {
		return
	}

	switch ch {
	case '$':
		// placeholder? $[0-9]+
		if lex.IsDigit(s.peek()) {
			s.scanPlaceholder(lval)
			return
		} else if s.scanDollarQuotedString(lval) {
			lval.SetID(SCONST)
			return
		}
		return

	case identQuote:
		// "[^"]"
		s.lastAttemptedID = int32(IDENT)
		s.quoted = true
		if s.scanString(lval, identQuote, false /* allowEscapes */, true /* requireUTF8 */) {
			lval.SetID(IDENT)
		}
		return

	case backtickQuote:
		if !s.backticks {
			return
		}
		s.lastAttemptedID = int32(IDENT)
		s.quoted = true
		if s.scanString(lval, backtickQuote, false /* allowEscapes */, true /* requireUTF8 */) {
			lval.SetID(IDENT)
		}
		return

	case singleQuote:
		// '[^']'
		s.lastAttemptedID = int32(SCONST)
		if s.scanString(lval, ch, false /* allowEscapes */, true /* requireUTF8 */) {
			lval.SetID(SCONST)
		}
		return

	case 'b':
		// Bytes?
		if s.peek() == singleQuote {
			// b'[^']'
			s.lastAttemptedID = int32(BCONST)
			s.pos++
			if s.scanString(lval, singleQuote, true /* allowEscapes */, false /* requireUTF8 */) {
				lval.SetID(BCONST)
			}
			return
		}
		s.scanIdent(lval)
		return

	case 'r', 'R':
		s.scanIdent(lval)
		return

	case 'e', 'E':
		// Escaped string?
		if s.peek() == singleQuote {
			// [eE]'[^']'
			s.lastAttemptedID = int32(SCONST)
			s.pos++
			if s.scanString(lval, singleQuote, true /* allowEscapes */, true /* requireUTF8 */) {
				lval.SetID(SCONST)
			}
			return
		}
		s.scanIdent(lval)
		return

	case 'B':
		// Bit array literal?
		if s.peek() == singleQuote {
			// B'[01]*'
			s.pos++
			s.scanBitString(lval, singleQuote)
			return
		}
		s.scanIdent(lval)
		return

	case 'x', 'X':
		// Hex literal?
		if s.peek() == singleQuote {
			// [xX]'[a-f0-9]'
			s.pos++
			s.scanHexString(lval, singleQuote)
			return
		}
		s.scanIdent(lval)
		return

	case '.':
		switch t := s.peek(); {
		case t == '.': // ..
			s.pos++
			lval.SetID(DOT_DOT)
			return
		case lex.IsDigit(t):
			s.lastAttemptedID = int32(FCONST)
			s.scanNumber(lval, ch)
			return
		}
		return

	case '!':
		switch s.peek() {
		case '=': // !=
			s.pos++
			lval.SetID(NOT_EQUALS)
			return
		case '~': // !~
			if s.peekN(1) == '~' { // !~~ and !~~* are NOT followed by the LIKE or ILIKE operator
				lval.SetID(NOT)
				return
			}
			s.pos++
			if s.peek() == '*' { // !~*
				s.pos++
				lval.SetID(NOT_REGIMATCH)
				return
			}
			lval.SetID(NOT_REGMATCH)
			return
		}
		return

	case '?':
		switch s.peek() {
		case '|': // ?|
			s.pos++
			lval.SetID(JSON_SOME_EXISTS)
			return
		case '&': // ?&
			s.pos++
			lval.SetID(JSON_ALL_EXISTS)
			return
		}
		return

	case '<':
		switch s.peek() {
		case '<': // <<
			s.pos++
			if s.peek() == '=' { // <<=
				s.pos++
				lval.SetID(INET_CONTAINED_BY_OR_EQUALS)
				return
			}
			lval.SetID(LSHIFT)
			return
		case '>': // <>
			s.pos++
			lval.SetID(NOT_EQUALS)
			return
		case '=': // <=
			s.pos++
			lval.SetID(LESS_EQUALS)
			return
		case '@': // <@
			s.pos++
			lval.SetID(CONTAINED_BY)
			return
		}
		return

	case '>':
		switch s.peek() {
		case '>': // >>
			s.pos++
			if s.peek() == '=' { // >>=
				s.pos++
				lval.SetID(INET_CONTAINS_OR_EQUALS)
				return
			}
			lval.SetID(RSHIFT)
			return
		case '=': // >=
			s.pos++
			lval.SetID(GREATER_EQUALS)
			return
		}
		return

	case ':':
		if s.peek() == ':' { // ::
			if s.peekN(1) == ':' {
				// :::
				s.pos += 2
				lval.SetID(TYPEANNOTATE)
				return
			}
			s.pos++
			lval.SetID(TYPECAST)
			return
		}
		return

	case '|':
		switch s.peek() {
		case '|': // ||
			s.pos++
			if s.peek() == '/' { // ||/
				s.pos++
				lval.SetID(CBRT)
				return
			}
			lval.SetID(CONCAT)
			return
		case '/': // |/
			s.pos++
			lval.SetID(SQRT)
			return
		}
		return

	case '/':
		if s.peek() == '/' { // //
			s.pos++
			lval.SetID(FLOORDIV)
			return
		}
		return

	case '~':
		switch s.peek() {
		case '*': // ~*
			s.pos++
			lval.SetID(REGIMATCH)
			return
		case '~': // ~~ and ~~* are PostgreSQL's operator spellings of LIKE and ILIKE
			s.pos++
			if s.peek() == '*' {
				s.pos++
				lval.SetID(ILIKE)
				return
			}
			lval.SetID(LIKE)
			return
		}
		return

	case '@':
		switch s.peek() {
		case '>': // @>
			s.pos++
			lval.SetID(CONTAINS)
			return
		case '@': // @@
			s.pos++
			lval.SetID(AT_AT)
			return
		}
		return

	case '&':
		if s.peek() == '&' { // &&
			s.pos++
			lval.SetID(AND_AND)
			return
		}
		return

	case '-':
		if s.peek() == '>' { // ->
			if s.peekN(1) == '>' {
				// ->>
				s.pos += 2
				lval.SetID(FETCHTEXT)
				return
			}
			s.pos++
			lval.SetID(FETCHVAL)
			return
		}
		return

	case '#':
		switch s.peek() {
		case '>': // #>
			if s.peekN(1) == '>' {
				// #>>
				s.pos += 2
				lval.SetID(FETCHTEXT_PATH)
				return
			}
			s.pos++
			lval.SetID(FETCHVAL_PATH)
			return
		case '-': // #-
			s.pos++
			lval.SetID(REMOVE_PATH)
			return
		}
		return

	default:
		if lex.IsDigit(ch) {
			s.lastAttemptedID = int32(ICONST)
			s.scanNumber(lval, ch)
			return
		}
		if lex.IsIdentStart(ch) {
			s.scanIdent(lval)
			return
		}
	}

	// Everything else is a single character token which we already initialized
	// lval for above.
}

func (s *scanner) peek() int {
	if s.pos >= len(s.in) {
		return eof
	}
	return int(s.in[s.pos])
}

func (s *scanner) peekN(n int) int {
	pos := s.pos + n
	if pos >= len(s.in) {
		return eof
	}
	return int(s.in[pos])
}

func (s *scanner) next() int {
	ch := s.peek()
	if ch != eof {
		s.pos++
	}
	return ch
}

func (s *scanner) skipWhitespace(lval *sqlSymType, allowComments bool) (newline, ok bool) {
	newline = false
	for {
		ch := s.peek()
		if ch == '\n' {
			s.pos++
			newline = true
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\f' {
			s.pos++
			continue
		}
		if allowComments {
			if present, cok := s.ScanComment(lval); !cok {
				return false, false
			} else if present {
				continue
			}
		}
		break
	}
	return newline, true
}

// ScanComment scans the input as a comment.
func (s *scanner) ScanComment(lval *sqlSymType) (present, ok bool) {
	start := s.pos
	ch := s.peek()

	if ch == '/' {
		s.pos++
		if s.peek() != '*' {
			s.pos--
			return false, true
		}
		s.pos++
		depth := 1
		for {
			switch s.next() {
			case '*':
				if s.peek() == '/' {
					s.pos++
					depth--
					if depth == 0 {
						return true, true
					}
					continue
				}

			case '/':
				if s.peek() == '*' {
					s.pos++
					depth++
					continue
				}

			case eof:
				lval.SetID(ERROR)
				lval.SetPos(int32(start)) // #nosec G115 -- ParseQuery rejects statements of 2 GiB or more
				lval.SetStr("unterminated comment")
				return false, false
			}
		}
	}

	if ch == '-' {
		s.pos++
		if s.peek() != '-' {
			s.pos--
			return false, true
		}
		for {
			switch s.next() {
			case eof, '\n':
				return true, true
			}
		}
	}

	return false, true
}

func (s *scanner) lowerCaseAndNormalizeIdent(lval *sqlSymType) {
	s.lastAttemptedID = int32(IDENT)
	s.pos--
	start := s.pos
	isASCII := true
	isLower := true

	// Consume the Scanner character by character, stopping after the last legal
	// identifier character. By the end of this function, we need to
	// lowercase and unicode normalize this identifier, which is expensive if
	// there are actual unicode characters in it. If not, it's quite cheap - and
	// if it's lowercase already, there's no work to do. Therefore, we keep track
	// of whether the string is only ASCII or only ASCII lowercase for later.
	for {
		ch := s.peek()
		if ch >= utf8.RuneSelf {
			isASCII = false
		} else if ch >= 'A' && ch <= 'Z' {
			isLower = false
		}

		if !lex.IsIdentMiddle(ch) {
			break
		}

		s.pos++
	}

	if isLower && isASCII {
		// Already lowercased - nothing to do.
		lval.SetStr(s.in[start:s.pos])
	} else if isASCII {
		// We know that the identifier we've seen so far is ASCII, so we don't need
		// to unicode normalize. Instead, just lowercase as normal.
		b := s.allocBytes(s.pos - start)
		_ = b[s.pos-start-1] // For bounds check elimination.
		for i, c := range s.in[start:s.pos] {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			b[i] = byte(c) // #nosec G115 -- this branch holds only ASCII identifiers
		}
		lval.SetStr(*(*string)(unsafe.Pointer(&b)))
	} else {
		// The string has unicode in it. No choice but to run Normalize.
		lval.SetStr(lex.NormalizeName(s.in[start:s.pos]))
	}
}

func (s *scanner) scanIdent(lval *sqlSymType) {
	s.lowerCaseAndNormalizeIdent(lval)

	lval.SetID(keywordID(lval.Str()))
}

func (s *scanner) scanNumber(lval *sqlSymType, ch int) {
	start := s.pos - 1
	isHex := false
	hasDecimal := ch == '.'
	hasExponent := false

	for {
		ch := s.peek()
		if (isHex && lex.IsHexDigit(ch)) || lex.IsDigit(ch) {
			s.pos++
			continue
		}
		if ch == 'x' || ch == 'X' {
			if isHex || s.in[start] != '0' || s.pos != start+1 {
				lval.SetID(ERROR)
				lval.SetStr(errInvalidHexNumeric)
				return
			}
			s.pos++
			isHex = true
			continue
		}
		if isHex {
			break
		}
		if ch == '.' {
			if hasDecimal || hasExponent {
				break
			}
			s.pos++
			if s.peek() == '.' {
				// Found ".." while scanning a number: back up to the end of the
				// integer.
				s.pos--
				break
			}
			hasDecimal = true
			continue
		}
		if ch == 'e' || ch == 'E' {
			if hasExponent {
				break
			}
			hasExponent = true
			s.pos++
			ch = s.peek()
			if ch == '-' || ch == '+' {
				s.pos++
			}
			ch = s.peek()
			if !lex.IsDigit(ch) {
				lval.SetID(ERROR)
				lval.SetStr("invalid floating point literal")
				return
			}
			continue
		}
		break
	}

	if s.durations && !isHex && !hasDecimal && !hasExponent && isASCIILetter(s.peek()) {
		s.scanDuration(lval, start)
		return
	}
	// Disallow identifier after numerical constants e.g. "124foo".
	if lex.IsIdentStart(s.peek()) {
		lval.SetID(ERROR)
		lval.SetStr(fmt.Sprintf("trailing junk after numeric literal at or near %q", s.in[start:s.pos+1]))
		return
	}

	lval.SetStr(s.in[start:s.pos])
	if hasDecimal || hasExponent {
		lval.SetID(FCONST)
		lval.SetUnionVal(ast.NewNumVal(lval.Str(), false /* negative */))
	} else {
		if isHex && s.pos == start+2 {
			lval.SetID(ERROR)
			lval.SetStr(errInvalidHexNumeric)
			return
		}

		// Strip off leading zeros from non-hex (decimal) literals so that
		// a later conversion doesn't inappropriately interpret the
		// string as an octal literal. Note: we can't use strings.TrimLeft
		// here, because it will truncate '0' to ''.
		if !isHex {
			for len(lval.Str()) > 1 && lval.Str()[0] == '0' {
				lval.SetStr(lval.Str()[1:])
			}
		}

		lval.SetID(ICONST)
		lval.SetUnionVal(ast.NewNumVal(lval.Str(), false /* negative */))
	}
}

func isASCIILetter(ch int) bool {
	return ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z')
}

func (s *scanner) scanDuration(lval *sqlSymType, start int) {
	for isASCIILetter(s.peek()) {
		s.pos++
	}
	if lex.IsIdentMiddle(s.peek()) {
		lval.SetID(ERROR)
		lval.SetStr(fmt.Sprintf("trailing junk after duration literal at or near %q", s.in[start:s.pos+1]))
		return
	}
	lval.SetID(DCONST)
	lval.SetStr(s.in[start:s.pos])
}

func (s *scanner) scanPlaceholder(lval *sqlSymType) {
	s.lastAttemptedID = int32(PLACEHOLDER)
	start := s.pos
	for lex.IsDigit(s.peek()) {
		s.pos++
	}
	lval.SetStr(s.in[start:s.pos])

	placeholder, err := ast.NewPlaceholder(lval.Str())
	if err != nil {
		lval.SetID(ERROR)
		lval.SetStr(err.Error())
		return
	}
	lval.SetID(PLACEHOLDER)
	lval.SetUnionVal(placeholder)
}

// scanHexString scans the content inside x'....'.
func (s *scanner) scanHexString(lval *sqlSymType, ch int) bool {
	s.lastAttemptedID = int32(BCONST)
	buf := s.buffer()

	var curbyte byte
	bytep := 0
	const errInvalidBytesLiteral = "invalid hexadecimal bytes literal"
outer:
	for {
		b := s.next()
		switch b {
		case ch:
			newline, ok := s.skipWhitespace(lval, false)
			if !ok {
				return false
			}
			// SQL allows joining adjacent strings separated by whitespace
			// as long as that whitespace contains at least one
			// newline. Kind of strange to require the newline, but that
			// is the standard.
			if s.peek() == ch && newline {
				s.pos++
				continue
			}
			break outer

		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			curbyte = (curbyte << 4) | byte(b-'0') // #nosec G115 -- b is a hex digit, so each value is below 16
		case 'a', 'b', 'c', 'd', 'e', 'f':
			curbyte = (curbyte << 4) | byte(b-'a'+10) // #nosec G115 -- as above
		case 'A', 'B', 'C', 'D', 'E', 'F':
			curbyte = (curbyte << 4) | byte(b-'A'+10) // #nosec G115 -- as above
		default:
			lval.SetID(ERROR)
			lval.SetStr(errInvalidBytesLiteral)
			return false
		}
		bytep++

		if bytep > 1 {
			buf = append(buf, curbyte)
			bytep = 0
			curbyte = 0
		}
	}

	if bytep != 0 {
		lval.SetID(ERROR)
		lval.SetStr(errInvalidBytesLiteral)
		return false
	}

	lval.SetID(BCONST)
	lval.SetStr(s.finishString(buf))
	return true
}

// scanBitString scans the content inside B'....'.
func (s *scanner) scanBitString(lval *sqlSymType, ch int) bool {
	s.lastAttemptedID = int32(BITCONST)
	buf := s.buffer()
outer:
	for {
		b := s.next()
		switch b {
		case ch:
			newline, ok := s.skipWhitespace(lval, false)
			if !ok {
				return false
			}
			// SQL allows joining adjacent strings separated by whitespace
			// as long as that whitespace contains at least one
			// newline. Kind of strange to require the newline, but that
			// is the standard.
			if s.peek() == ch && newline {
				s.pos++
				continue
			}
			break outer

		case '0', '1':
			buf = append(buf, byte(b))
		default:
			lval.SetID(ERROR)
			lval.SetStr(fmt.Sprintf(`"%c" is not a valid binary digit`, rune(b))) // #nosec G115 -- the value is one single-byte character
			return false
		}
	}

	lval.SetID(BITCONST)
	lval.SetStr(s.finishString(buf))
	return true
}

// scanString scans the content inside '...'. This is used for simple
// string literals '...' but also e'....' and b'...'. For x'...', see
// scanHexString().
func (s *scanner) scanString(lval *sqlSymType, ch int, allowEscapes, requireUTF8 bool) bool {
	buf := s.buffer()
	var runeTmp [utf8.UTFMax]byte
	start := s.pos
outer:
	for {
		switch s.next() {
		case ch:
			buf = append(buf, s.in[start:s.pos-1]...)
			if s.peek() == ch {
				// Double quote is translated into a single quote that is part of the
				// string.
				start = s.pos
				s.pos++
				continue
			}

			newline, ok := s.skipWhitespace(lval, false)
			if !ok {
				return false
			}

			// SQL allows joining adjacent single-quoted strings separated by
			// whitespace as long as that whitespace contains at least one
			// newline. Kind of strange to require the newline, but that is the
			// standard.
			if ch == singleQuote && s.peek() == singleQuote && newline {
				s.pos++
				start = s.pos
				continue
			}
			break outer

		case '\\':
			t := s.peek()

			if allowEscapes {
				buf = append(buf, s.in[start:s.pos-1]...)
				if t == ch {
					start = s.pos
					s.pos++
					continue
				}

				switch t {
				case 'a', 'b', 'f', 'n', 'r', 't', 'v', 'x', 'X', 'u', 'U', '\\',
					'0', '1', '2', '3', '4', '5', '6', '7':
					var tmp string
					if t == 'X' && len(s.in[s.pos:]) >= 3 {
						// UnquoteChar doesn't handle 'X' so we create a temporary string
						// for it to parse.
						tmp = "\\x" + s.in[s.pos+1:s.pos+3]
					} else {
						tmp = s.in[s.pos-1:]
					}
					v, multibyte, tail, err := strconv.UnquoteChar(tmp, byte(ch)) // #nosec G115 -- the value is one single-byte character
					if err != nil {
						lval.SetID(ERROR)
						lval.SetStr(err.Error())
						return false
					}
					if v < utf8.RuneSelf || !multibyte {
						buf = append(buf, byte(v)) // #nosec G115 -- v is a single byte here
					} else {
						n := utf8.EncodeRune(runeTmp[:], v)
						buf = append(buf, runeTmp[:n]...)
					}
					s.pos += len(tmp) - len(tail) - 1
					start = s.pos
					continue
				}

				// If we end up here, it's a redundant escape - simply drop the
				// backslash. For example, e'\"' is equivalent to e'"', and
				// e'\d\b' to e'd\b'. This is what Postgres does:
				// http://www.postgresql.org/docs/9.4/static/sql-syntax-lexical.html#SQL-SYNTAX-STRINGS-ESCAPE
				start = s.pos
			}

		case eof:
			lval.SetID(ERROR)
			lval.SetStr(errUnterminated)
			return false
		}
	}

	if requireUTF8 && !utf8.Valid(buf) {
		lval.SetID(ERROR)
		lval.SetStr(errInvalidUTF8)
		return false
	}

	if ch == identQuote || ch == backtickQuote {
		lval.SetStr(lex.NormalizeString(s.finishString(buf)))
	} else {
		lval.SetStr(s.finishString(buf))
	}
	return true
}

// scanDollarQuotedString scans for so called dollar-quoted strings, which start/end with either $$ or $tag$, where
// tag is some arbitrary string.  e.g. $$a string$$ or $escaped$a string$escaped$.
func (s *scanner) scanDollarQuotedString(lval *sqlSymType) bool {
	s.lastAttemptedID = int32(SCONST)
	buf := s.buffer()
	start := s.pos

	foundStartTag := false
	possibleEndTag := false
	startTagIndex := -1
	var startTag string

outer:
	for {
		ch := s.peek()
		switch ch {
		case '$':
			s.pos++
			if foundStartTag {
				if possibleEndTag {
					if len(startTag) == startTagIndex {
						// Found end tag.
						buf = append(buf, s.in[start+len(startTag)+1:s.pos-len(startTag)-2]...)
						break outer
					}
					// Was not the end tag but the current $ might be the start of the end tag we are looking for, so
					// just reset the startTagIndex.
					startTagIndex = 0
				} else {
					possibleEndTag = true
					startTagIndex = 0
				}
			} else {
				startTag = s.in[start : s.pos-1]
				foundStartTag = true
			}

		case eof:
			if foundStartTag {
				// A start tag was found, therefore we expect an end tag before the eof, otherwise it is an error.
				lval.SetID(ERROR)
				lval.SetStr(errUnterminated)
			} else {
				// This is not a dollar-quoted string, reset the pos back to the start.
				s.pos = start
			}
			return false

		default:
			// If we haven't found a start tag yet, check whether the current characters is a valid for a tag.
			if !foundStartTag && !lex.IsIdentStart(ch) && !lex.IsDigit(ch) {
				return false
			}
			s.pos++
			if possibleEndTag {
				// Check whether this could be the end tag.
				if startTagIndex >= len(startTag) || ch != int(startTag[startTagIndex]) {
					// This is not the end tag we are looking for.
					possibleEndTag = false
					startTagIndex = -1
				} else {
					startTagIndex++
				}
			}
		}
	}

	if !utf8.Valid(buf) {
		lval.SetID(ERROR)
		lval.SetStr(errInvalidUTF8)
		return false
	}

	lval.SetStr(s.finishString(buf))
	return true
}
