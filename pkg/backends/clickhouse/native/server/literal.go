/*
 * Copyright 2026 The Trickster Authors
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

package server

import (
	"errors"
	"fmt"
	"strings"
)

var errBadLiteral = errors.New("invalid ClickHouse text literal")

// ParseTextLiteral parses ClickHouse's text form of compound values, as
// printed in TSV/CSV output: arrays "[1,'a']", tuples "('a',1)", maps
// "{'k':1}", quoted strings with backslash escapes, and NULL. Scalars come
// back as strings (or nil for NULL) so the column codec can convert them.
func ParseTextLiteral(text string) (any, error) {
	p := &literalParser{s: text}
	p.skipSpace()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.i != len(p.s) {
		return nil, fmt.Errorf("%w: trailing data in %q", errBadLiteral, text)
	}
	return v, nil
}

type literalParser struct {
	s string
	i int
}

func (p *literalParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *literalParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("%w: unexpected end", errBadLiteral)
	}
	switch p.s[p.i] {
	case '[':
		return p.sequence(']')
	case '(':
		return p.sequence(')')
	case '{':
		return p.mapping()
	case '\'':
		return p.quoted()
	}
	start := p.i
	for p.i < len(p.s) && !strings.ContainsRune(",])}:", rune(p.s[p.i])) {
		p.i++
	}
	token := strings.TrimSpace(p.s[start:p.i])
	if token == "NULL" {
		return nil, nil
	}
	if token == "" {
		return nil, fmt.Errorf("%w: empty element at %d", errBadLiteral, start)
	}
	return token, nil
}

func (p *literalParser) sequence(closer byte) ([]any, error) {
	p.i++ // opener
	out := []any{}
	for {
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == closer {
			p.i++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, fmt.Errorf("%w: unterminated sequence", errBadLiteral)
		}
		if p.s[p.i] == ',' {
			p.i++
		}
	}
}

func (p *literalParser) mapping() (map[string]any, error) {
	p.i++ // '{'
	out := map[string]any{}
	for {
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return out, nil
		}
		k, err := p.value()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, fmt.Errorf("%w: expected ':' in map", errBadLiteral)
		}
		p.i++
		p.skipSpace()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out[fmt.Sprint(k)] = v
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, fmt.Errorf("%w: unterminated map", errBadLiteral)
		}
		if p.s[p.i] == ',' {
			p.i++
		}
	}
}

func (p *literalParser) quoted() (string, error) {
	p.i++ // opening quote
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		switch c {
		case '\'':
			return b.String(), nil
		case '\\':
			if p.i >= len(p.s) {
				return "", fmt.Errorf("%w: dangling escape", errBadLiteral)
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", fmt.Errorf("%w: unterminated string", errBadLiteral)
}
