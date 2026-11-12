// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in licenses/BSD-vitess.txt.

// Portions of this file are additionally subject to the following
// license and copyright.
//
// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// This code was derived from https://github.com/youtube/vitess.

// Modified by The Trickster Authors in 2026: reduced to what Trickster's SQL formatter and scanner use.

// Package lexbase contains utilities for lexing sql.
package lex

import (
	"bytes"
	"unicode/utf8"
)

// EncodeFlags influence the formatting of strings and identifiers.
type EncodeFlags int

// HasFlags tests whether the given flags are set.
func (f EncodeFlags) HasFlags(subset EncodeFlags) bool {
	return f&subset == subset
}

const (
	// EncNoFlags indicates nothing special should happen while encoding.
	EncNoFlags EncodeFlags = 0

	// EncBareStrings indicates that strings will be rendered without
	// wrapping quotes if they contain no special characters.
	EncBareStrings EncodeFlags = 1 << iota

	// EncBareIdentifiers indicates that identifiers will be rendered
	// without wrapping quotes.
	EncBareIdentifiers

	// EncBacktickIdentifiers quotes identifiers with backticks instead of double quotes.
	EncBacktickIdentifiers
)

// EncodeRestrictedSQLIdent writes the identifier in s to buf. The
// identifier is quoted if either the flags ask for it, the identifier
// contains special characters, or the identifier is a reserved SQL
// keyword.
func EncodeRestrictedSQLIdent(buf *bytes.Buffer, s string, flags EncodeFlags) {
	if flags.HasFlags(EncBareIdentifiers) || (!isReservedKeyword(s) && IsBareIdentifier(s)) {
		buf.WriteString(s)
		return
	}
	encodeQuotedIdent(buf, s, identQuoteFor(flags))
}

// EncodeUnrestrictedSQLIdent writes the identifier in s to buf.
// The identifier is only quoted if the flags don't tell otherwise and
// the identifier contains special characters.
func EncodeUnrestrictedSQLIdent(buf *bytes.Buffer, s string, flags EncodeFlags) {
	if flags.HasFlags(EncBareIdentifiers) || (!quotedWord(s) && IsBareIdentifier(s)) {
		buf.WriteString(s)
		return
	}
	encodeQuotedIdent(buf, s, identQuoteFor(flags))
}

const (
	doubleQuote = '"'
	backtick    = '`'
)

func identQuoteFor(flags EncodeFlags) byte {
	if flags.HasFlags(EncBacktickIdentifiers) {
		return backtick
	}
	return doubleQuote
}

func encodeQuotedIdent(buf *bytes.Buffer, s string, quote byte) {
	buf.WriteByte(quote)
	start := 0
	for i, n := 0, len(s); i < n; i++ {
		ch := s[i]
		// The only character that requires escaping is the quote itself.
		if ch == quote {
			if start != i {
				buf.WriteString(s[start:i])
			}
			start = i + 1
			buf.WriteByte(ch)
			buf.WriteByte(ch) // add extra copy of ch
		}
	}
	if start < len(s) {
		buf.WriteString(s[start:])
	}
	buf.WriteByte(quote)
}

var mustQuoteMap = map[byte]bool{
	' ': true,
	',': true,
	'{': true,
	'}': true,
}

// EncodeStandardSQLString writes a string literal in the standard form, quotes doubled and other
// characters as they are, falling back to an escape string for control characters
func EncodeStandardSQLString(buf *bytes.Buffer, in string, flags EncodeFlags) {
	if flags.HasFlags(EncBareStrings) || needsEscapeString(in) {
		EncodeSQLStringWithFlags(buf, in, flags)
		return
	}
	buf.WriteByte('\'')
	for i := range len(in) {
		if in[i] == '\'' {
			buf.WriteByte('\'')
		}
		buf.WriteByte(in[i])
	}
	buf.WriteByte('\'')
}

// EscapeStandardSQLString returns a string literal in the standard form, as EncodeStandardSQLString writes it
func EscapeStandardSQLString(in string) string {
	var buf bytes.Buffer
	EncodeStandardSQLString(&buf, in, EncNoFlags)
	return buf.String()
}

func needsEscapeString(in string) bool {
	for i := range len(in) {
		switch c := in[i]; {
		case c == '\t' || c == '\n' || c == '\r':
		case c < ' ' || c == 0x7f:
			return true
		}
	}
	return false
}

// EncodeSQLStringWithFlags writes a string literal to buf. All
// unicode and non-printable characters are escaped. flags controls
// the output format: if encodeBareString is set, the output string
// will not be wrapped in quotes if the strings contains no special
// characters.
func EncodeSQLStringWithFlags(buf *bytes.Buffer, in string, flags EncodeFlags) {
	// See http://www.postgresql.org/docs/9.4/static/sql-syntax-lexical.html
	start := 0
	escapedString := false
	bareStrings := flags.HasFlags(EncBareStrings)
	// Loop through each unicode code point.
	for i, r := range in {
		if i < start {
			continue
		}
		ch := byte(r) // #nosec G115 -- only read below for printable ASCII
		if r >= 0x20 && r < 0x7F {
			if mustQuoteMap[ch] {
				// We have to quote this string - ignore bareStrings setting
				bareStrings = false
			}
			if _, escaped := escapeChar(ch); !escaped && ch != '\'' {
				continue
			}
		}

		if !escapedString {
			buf.WriteString("e'") // begin e'xxx' string
			escapedString = true
		}
		buf.WriteString(in[start:i])
		ln := utf8.RuneLen(r)
		if ln < 0 {
			start = i + 1
		} else {
			start = i + ln
		}
		EncodeEscapedChar(buf, in, r, ch, i, '\'')
	}

	quote := !escapedString && !bareStrings
	if quote {
		buf.WriteByte('\'') // begin 'xxx' string if nothing was escaped
	}
	if start < len(in) {
		buf.WriteString(in[start:])
	}
	if escapedString || quote {
		buf.WriteByte('\'')
	}
}

// EncodeSQLBytes encodes the SQL byte array in 'in' to buf, to a
// format suitable for re-scanning. We don't use a straightforward hex
// encoding here with x'...'  because the result would be less
// compact. We are trading a little more time during the encoding to
// have a little less bytes on the wire.
func EncodeSQLBytes(buf *bytes.Buffer, in string) {
	buf.WriteString("b'")
	EncodeSQLBytesInner(buf, in)
	buf.WriteByte('\'')
}

// EncodeSQLBytesInner is like EncodeSQLBytes but does not include the
// outer quote delimiter and the 'b' prefix.
func EncodeSQLBytesInner(buf *bytes.Buffer, in string) {
	start := 0
	// Loop over the bytes of the string (i.e., don't use range over unicode
	// code points).
	for i, n := 0, len(in); i < n; i++ {
		ch := in[i]
		if encodedChar, ok := escapeChar(ch); ok {
			buf.WriteString(in[start:i])
			buf.WriteByte('\\')
			buf.WriteByte(encodedChar)
			start = i + 1
		} else if ch == '\'' {
			// escapeChar leaves the quote out because it also serves strings
			// that aren't quoted with single-quotes
			buf.WriteString(in[start:i])
			buf.WriteByte('\\')
			buf.WriteByte(ch)
			start = i + 1
		} else if ch < 0x20 || ch >= 0x7F {
			buf.WriteString(in[start:i])
			// Escape non-printable characters.
			writeHexByte(buf, ch)
			start = i + 1
		}
	}
	buf.WriteString(in[start:])
}
