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

package cockroach

import (
	"regexp"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

var boundPlaceholder = regexp.MustCompile(`<\$[A-Za-z0-9_]+\$>`)

// MaskPlaceholders blanks each time-bound placeholder in place, moving no byte offset.
// Its dollar signs would otherwise scan as the start of a dollar-quoted string.
func MaskPlaceholders(sql string) string {
	return boundPlaceholder.ReplaceAllStringFunc(sql, func(token string) string {
		return strings.Repeat("0", len(token))
	})
}

// FindKeywords returns the byte span of the first run of the given keywords
// outside parentheses, quotes and comments. Bound placeholders are tolerated.
func FindKeywords(sql string, keywords ...string) (start, end int, ok bool) {
	if len(keywords) == 0 {
		return 0, 0, false
	}
	scanner := sqlscan.New(MaskPlaceholders(sql), sqlscan.Options{})
	matched := 0
	for {
		token, more := scanner.Next()
		if !more {
			return 0, 0, false
		}
		switch {
		case token.Depth == 0 && scanner.IsWord(token, keywords[matched]):
			if matched == 0 {
				start = token.Start
			}
			matched++
		case token.Depth == 0 && scanner.IsWord(token, keywords[0]):
			start, matched = token.Start, 1
		default:
			matched = 0
		}
		if matched == len(keywords) {
			return start, token.End, true
		}
	}
}

// InsertClause puts a lifted clause back into rendered SQL, before the first
// top-level phrase present (such as "ORDER BY"), or at the end when none is.
func InsertClause(rendered, clause string, before ...string) string {
	at := len(rendered)
	for _, phrase := range before {
		if start, _, ok := FindKeywords(rendered, strings.Fields(phrase)...); ok && start < at {
			at = start
		}
	}
	head := strings.TrimRight(rendered[:at], " ")
	if at == len(rendered) {
		return head + " " + clause
	}
	return head + " " + clause + " " + rendered[at:]
}
