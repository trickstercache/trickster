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

package model

import (
	"strings"

	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// literalJSON converts ClickHouse's text of a compound value, as TSV writes an Array, Map or Tuple,
// to the JSON ClickHouse's JSON format writes for it
type literalJSON struct {
	s   string
	i   int
	out []byte
	buf []byte
}

// appendLiteralJSON appends the JSON of a compound value's text, a named Tuple's as an object of names,
// and reports false, having appended nothing, for text that isn't a literal
func appendLiteralJSON(b []byte, text string, names []string) ([]byte, bool) {
	p := literalJSON{s: text, out: b}
	if !p.value(names) || p.i != len(p.s) {
		return b, false
	}
	return p.out, true
}

func (p *literalJSON) value(names []string) bool {
	if p.i >= len(p.s) {
		return false
	}
	switch p.s[p.i] {
	case '[':
		return p.sequence(']', nil)
	case '(':
		return p.sequence(')', names)
	case '{':
		return p.mapping()
	case '\'':
		text, ok := p.quoted()
		p.out = tstrings.AppendJSON(p.out, text)
		return ok
	}
	return p.bare()
}

// sequence reads an Array's or Tuple's elements up to closer, as an array, or as an object of names
func (p *literalJSON) sequence(closer byte, names []string) bool {
	p.i++
	open, end := byte('['), byte(']')
	if names != nil {
		open, end = '{', '}'
	}
	p.out = append(p.out, open)
	for n := 0; ; n++ {
		if p.i < len(p.s) && p.s[p.i] == closer {
			p.i++
			p.out = append(p.out, end)
			return true
		}
		if n > 0 {
			if p.i >= len(p.s) || p.s[p.i] != ',' {
				return false
			}
			p.i++
			p.out = append(p.out, ',')
		}
		if names != nil {
			if n >= len(names) {
				return false
			}
			p.out = append(tstrings.AppendJSON(p.out, names[n]), ':')
		}
		if !p.value(nil) {
			return false
		}
	}
}

// mapping reads a Map's entries as an object, its keys as JSON strings
func (p *literalJSON) mapping() bool {
	p.i++
	p.out = append(p.out, '{')
	for n := 0; ; n++ {
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			p.out = append(p.out, '}')
			return true
		}
		if n > 0 {
			if p.i >= len(p.s) || p.s[p.i] != ',' {
				return false
			}
			p.i++
			p.out = append(p.out, ',')
		}
		if p.i >= len(p.s) {
			return false
		}
		var key string
		if p.s[p.i] == '\'' {
			k, ok := p.quoted()
			if !ok {
				return false
			}
			key = k
		} else {
			start := p.i
			for p.i < len(p.s) && p.s[p.i] != ':' {
				p.i++
			}
			key = p.s[start:p.i]
		}
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return false
		}
		p.i++
		p.out = append(tstrings.AppendJSON(p.out, key), ':')
		if !p.value(nil) {
			return false
		}
	}
}

// quoted reads a single-quoted string, without its backslash escapes
func (p *literalJSON) quoted() (string, bool) {
	p.i++
	p.buf = p.buf[:0]
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		switch c {
		case '\'':
			return string(p.buf), true
		case '\\':
			if p.i >= len(p.s) {
				return "", false
			}
			c = p.s[p.i]
			p.i++
			switch c {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case 'r':
				c = '\r'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case '0':
				c = 0
			}
		}
		p.buf = append(p.buf, c)
	}
	return "", false
}

// bare reads an unquoted element: NULL, a bool, or a number, with NaN and the infinities as null
func (p *literalJSON) bare() bool {
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune(",])}:", rune(p.s[p.i])) {
		p.i++
	}
	switch tok := p.s[start:p.i]; tok {
	case "":
		return false
	case "NULL", "nan", "-nan", "inf", "+inf", "-inf":
		p.out = append(p.out, jsonNull...)
	case "true", "false":
		p.out = append(p.out, tok...)
	default:
		if !isJSONNumber(tok) {
			return false
		}
		p.out = append(p.out, tok...)
	}
	return true
}

// tupleNames returns the element names of a named Tuple type, unwrapped of Nullable and
// LowCardinality, or nil for any other type
func tupleNames(typ string) []string {
	typ = unwrapColumnType(typ)
	inner, ok := cutWrapper(typ, "Tuple(")
	if !ok {
		return nil
	}
	elems := splitTypeList(inner)
	names := make([]string, len(elems))
	for i, e := range elems {
		name, rest, found := strings.Cut(e, " ")
		// an unnamed element's type is its whole text, which holds no space or holds a parenthesis first
		if !found || strings.ContainsAny(name, "(") || rest == "" {
			return nil
		}
		names[i] = strings.Trim(name, "`")
	}
	return names
}
