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
	"bytes"
	"testing"
)

const dottedI = "\u0130"

func TestEncodeIdentifiers(t *testing.T) {
	for _, test := range []struct {
		in, restricted, unrestricted string
	}{
		{"abc", "abc", "abc"},
		{"select", `"select"`, "select"},
		{"Abc", `"Abc"`, `"Abc"`},
		{"a b", `"a b"`, `"a b"`},
		{`a"b`, `"a""b"`, `"a""b"`},
		{"", `""`, `""`},
		{"1a", `"1a"`, `"1a"`},
		{"\u00e9", "\u00e9", "\u00e9"},
		{"e\u0301", "\"e\u0301\"", "\"e\u0301\""},
		{"between", `"between"`, "between"},
	} {
		var buf bytes.Buffer
		EncodeRestrictedSQLIdent(&buf, test.in, EncNoFlags)
		if got := buf.String(); got != test.restricted {
			t.Errorf("restricted %q = %q, want %q", test.in, got, test.restricted)
		}
		buf.Reset()
		EncodeUnrestrictedSQLIdent(&buf, test.in, EncNoFlags)
		if got := buf.String(); got != test.unrestricted {
			t.Errorf("unrestricted %q = %q, want %q", test.in, got, test.unrestricted)
		}
		buf.Reset()
		EncodeRestrictedSQLIdent(&buf, test.in, EncBareIdentifiers)
		if got := buf.String(); got != test.in {
			t.Errorf("bare %q = %q", test.in, got)
		}
	}
}

func TestEncodeStrings(t *testing.T) {
	for in, want := range map[string]string{
		"abc": "'abc'", "it's": `e'it\'s'`, "a\nb": `e'a\nb'`, "\x01": `e'\x01'`,
		"\u2028": `e'\U00002028'`, "\U0001F600": `e'\U0001F600'`, "a\\b": `e'a\\b'`, "\xff": `e'\xff'`,
	} {
		var buf bytes.Buffer
		EncodeSQLStringWithFlags(&buf, in, EncNoFlags)
		if got := buf.String(); got != want {
			t.Errorf("string %q = %s, want %s", in, got, want)
		}
	}
	var buf bytes.Buffer
	EncodeSQLStringWithFlags(&buf, "abc", EncBareStrings)
	if buf.String() != "abc" {
		t.Errorf("bare string: %s", buf.String())
	}
	buf.Reset()
	EncodeSQLStringWithFlags(&buf, "a b", EncBareStrings)
	if buf.String() != "'a b'" {
		t.Errorf("bare string with a space: %s", buf.String())
	}
	buf.Reset()
	EncodeSQLBytes(&buf, "a\x00\xff'")
	if got := buf.String(); got != `b'a\x00\xff\''` {
		t.Errorf("bytes: %s", got)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"ABC": "abc", dottedI + "x": "ix", "\u0131": "i", "E\u0301": "\u00e9",
	} {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	if NormalizeString("e\u0301") != "\u00e9" || NormalizeString("abc") != "abc" {
		t.Error("NormalizeString")
	}
	for in, want := range map[string]bool{"abc": true, "a_1$": true, "Abc": false, "aBc": false, "1a": false, "": false, "a-b": false, "\u00e9": true, "e\u0301": false} {
		if got := IsBareIdentifier(in); got != want {
			t.Errorf("IsBareIdentifier(%q) = %v", in, got)
		}
	}
	if !IsHexDigit('f') || IsHexDigit('g') || !IsHexDigit('A') || !IsDigit('0') || IsDigit('a') {
		t.Error("digit predicates")
	}
	if !reservedKeyword("select") || reservedKeyword("abc") || !isReservedKeyword("between") {
		t.Error("keyword predicates")
	}
}

func TestEncodeStandardStrings(t *testing.T) {
	for in, want := range map[string]string{
		"abc": "'abc'", "it's": "'it''s'", "a\nb\tc": "'a\nb\tc'", "é\U0001F600": "'é\U0001F600'",
		`a\b`: `'a\b'`, "a\x01b": `e'a\x01b'`, "\x7f": `e'\x7f'`,
	} {
		if got := EscapeStandardSQLString(in); got != want {
			t.Errorf("standard string %q = %s, want %s", in, got, want)
		}
	}
	var buf bytes.Buffer
	EncodeStandardSQLString(&buf, "abc", EncBareStrings)
	if buf.String() != "abc" {
		t.Errorf("bare string: %s", buf.String())
	}
	for in, want := range map[string]string{"bucket": `"bucket"`, "verbose": `"verbose"`, "plain": "plain"} {
		buf.Reset()
		EncodeUnrestrictedSQLIdent(&buf, in, EncNoFlags)
		if buf.String() != want {
			t.Errorf("unrestricted %q = %s, want %s", in, buf.String(), want)
		}
	}
}
