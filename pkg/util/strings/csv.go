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

package strings

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// AppendCSVField appends field as encoding/csv writes it (ASCII Comma, no CRLF): quoted, quotes
// doubled, when it holds comma, a quote, CR or LF, is `\.`, or starts with a space
func AppendCSVField(dst []byte, field string, comma byte) []byte {
	if !csvFieldNeedsQuotes(field, comma) {
		return append(dst, field...)
	}
	dst = append(dst, '"')
	for len(field) > 0 {
		i := strings.IndexByte(field, '"')
		if i < 0 {
			return append(append(dst, field...), '"')
		}
		dst = append(dst, field[:i+1]...)
		dst = append(dst, '"')
		field = field[i+1:]
	}
	return append(dst, '"')
}

func csvFieldNeedsQuotes(field string, comma byte) bool {
	if field == "" {
		return false
	}
	if field == `\.` {
		return true
	}
	for i := range len(field) {
		if c := field[i]; c == '\n' || c == '\r' || c == '"' || c == comma {
			return true
		}
	}
	r, _ := utf8.DecodeRuneInString(field)
	return unicode.IsSpace(r)
}
