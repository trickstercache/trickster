// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in licenses/BSD-vitess.txt.

// Portions of this file are additionally subject to the following
// license and copyright.
//
// Copyright 2017 The Cockroach Authors.
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

package lex

import (
	"bytes"
	"unicode/utf8"
)

// This is its own package so it can be shared among packages that parser
// depends on.

// escapeChar returns the letter a backslash escape writes for a byte, such as n for a newline
func escapeChar(ch byte) (byte, bool) {
	switch ch {
	case '\b':
		return 'b', true
	case '\f':
		return 'f', true
	case '\n':
		return 'n', true
	case '\r':
		return 'r', true
	case '\t':
		return 't', true
	case '\\':
		return '\\', true
	}
	return 0, false
}

// writeHexByte writes a byte as a \xNN escape
func writeHexByte(buf *bytes.Buffer, ch byte) {
	buf.WriteString(`\x`)
	buf.WriteByte(lowerHex[ch>>4])
	buf.WriteByte(lowerHex[ch&0xf])
}

// EncodeEscapedChar is used internally to write out a character from a larger
// string that needs to be escaped to a buffer.
func EncodeEscapedChar(
	buf *bytes.Buffer,
	entireString string,
	currentRune rune,
	currentByte byte,
	currentIdx int,
	quoteChar byte,
) {
	ln := utf8.RuneLen(currentRune)
	if currentRune == utf8.RuneError {
		// Errors are due to invalid unicode points, so escape the bytes.
		// Make sure this is run at least once in case ln == -1.
		writeHexByte(buf, entireString[currentIdx])
		for ri := 1; ri < ln; ri++ {
			if currentIdx+ri < len(entireString) {
				writeHexByte(buf, entireString[currentIdx+ri])
			}
		}
	} else if ln == 1 {
		// For single-byte runes, do the same as encodeSQLBytes.
		if encodedChar, ok := escapeChar(currentByte); ok {
			buf.WriteByte('\\')
			buf.WriteByte(encodedChar)
		} else if currentByte == quoteChar {
			buf.WriteByte('\\')
			buf.WriteByte(quoteChar)
		} else {
			// Escape non-printable characters.
			writeHexByte(buf, currentByte)
		}
	} else {
		writeMultibyteRuneAsHex(buf, currentRune, ln)
	}
}

const (
	uppercaseHex = `0123456789ABCDEF`
	lowerHex     = `0123456789abcdef`
)

// writeMultibyteRuneAsHex is equivalent to either
// fmt.FPrintf(`\u%04X`) or fmt.FPrintf(`\U%08X`).
// We can't quite just use strconv since we need uppercase hex.
func writeMultibyteRuneAsHex(buf *bytes.Buffer, r rune, ln int) {
	if ln == 2 {
		buf.WriteString(`\u0000`)
	} else {
		buf.WriteString(`\U00000000`)
	}
	for i := 1; r > 0; r >>= 4 {
		buf.Bytes()[buf.Len()-i] = uppercaseHex[r&0x0f]
		i++
	}
}
