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

// Package sqlguard finds dialect-defined function and keyword hazards without
// depending on whether a statement is accepted by an AST parser.
package sqlguard

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

// WordClass describes the effect of a guarded word.
type WordClass uint8

const (
	// Volatile marks a call whose result or side effects vary between requests.
	Volatile WordClass = iota + 1
	// Clock marks a clock-reading call, which may be resolved in a time bound.
	Clock
	// BareClock marks a clock read written without parentheses.
	BareClock
	// Unfaithful marks a keyword or type the formatter cannot preserve.
	Unfaithful
	// Gapfill marks a call that generates missing buckets from the requested range.
	Gapfill
	// Carry marks a call that depends on values outside its bucket.
	Carry
	// SetReturning marks a call that can emit multiple rows.
	SetReturning
)

// Facts summarizes hazards outside strings and comments.
type Facts struct {
	Volatile     bool
	Clock        bool
	Unfaithful   bool
	Gapfill      bool
	Carries      bool
	SetReturning bool
}

const unicodeEscapePrefix = "u&"

// Scan reads words using a dialect's immutable lower-case table. Calls require
// an opening parenthesis; quoted lower-case function names are recognized too.
func Scan(sql string, words map[string]WordClass) Facts {
	var facts Facts
	scanner := sqlscan.New(sql, sqlscan.Options{})
	pending := WordClass(0)
	for {
		token, more := scanner.Next()
		if !more {
			return facts
		}
		if token.Kind == sqlscan.Punct && sql[token.Start] == '(' {
			switch pending {
			case Volatile:
				facts.Volatile = true
			case Clock:
				facts.Clock = true
			case Gapfill:
				facts.Gapfill = true
			case Carry:
				facts.Carries = true
			case SetReturning:
				facts.SetReturning = true
			}
		}
		pending = 0
		switch token.Kind {
		case sqlscan.Word:
			word := sql[token.Start:token.End]
			class, ok := words[word]
			if !ok {
				class = words[strings.ToLower(word)]
			}
			switch class {
			case BareClock:
				facts.Clock = true
			case Unfaithful:
				facts.Unfaithful = true
			default:
				pending = class
			}
		case sqlscan.QuotedIdent:
			if class := words[strings.Trim(sql[token.Start:token.End], `"`)]; class != Unfaithful {
				pending = class
			}
			fallthrough
		case sqlscan.String:
			if token.End-token.Start > len(unicodeEscapePrefix) &&
				strings.EqualFold(sql[token.Start:token.Start+len(unicodeEscapePrefix)], unicodeEscapePrefix) {
				facts.Unfaithful = true
			}
		}
	}
}
