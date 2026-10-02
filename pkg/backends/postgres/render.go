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

package postgres

import (
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

const (
	punctOpen  = "("
	punctClose = ")"
	punctComma = ","
	fromField  = " FROM"
	wordSystem = "system_user"
)

var errUnrenderable = errors.New("statement has no faithful PostgreSQL re-spelling")

var typeSpellings = map[string]string{
	// type names PostgreSQL does not know, in the upper case no unquoted identifier has
	"STRING": "text", "BYTES": "bytea",
}

var bareFunctions = map[string]struct{}{
	// SQL-value functions PostgreSQL only accepts without parentheses
	"current_timestamp": {}, "current_date": {}, "current_time": {}, "localtimestamp": {}, "localtime": {},
	"current_user": {}, "session_user": {}, "current_role": {}, "current_catalog": {}, "current_schema": {},
}

var unquotedReserved = map[string]struct{}{
	// reserved by PostgreSQL but left unquoted by the formatter, which never
	// writes them as keywords in a SELECT, so each occurrence is an identifier
	"binary": {}, "freeze": {}, "tablesample": {}, "verbose": {},
}

type textEdit struct {
	start, end int
	text       string
}

func postRender(rendered string) (string, error) {
	// re-spells what the parser's formatter writes in a way PostgreSQL rejects.
	masked := cockroach.MaskPlaceholders(rendered)
	scanner := sqlscan.New(masked, sqlscan.Options{})
	var tokens []sqlscan.Token
	for {
		token, more := scanner.Next()
		if !more {
			break
		}
		tokens = append(tokens, token)
	}
	text := func(i int) string {
		if i >= len(tokens) {
			return ""
		}
		return masked[tokens[i].Start:tokens[i].End]
	}
	var edits []textEdit
	for i, token := range tokens {
		if token.Kind != sqlscan.Word {
			continue
		}
		word := text(i)
		if spelling, ok := typeSpellings[word]; ok {
			edits = append(edits, textEdit{token.Start, token.End, spelling})
			continue
		}
		if _, ok := unquotedReserved[word]; ok {
			edits = append(edits, textEdit{token.Start, token.End, `"` + word + `"`})
			continue
		}
		switch {
		case word == wordSystem:
			// a column of this name and the SQL-value function render alike
			return "", errUnrenderable
		case word == fnExtract && text(i+1) == punctOpen && i+3 < len(tokens) &&
			tokens[i+2].Kind == sqlscan.String && text(i+3) == punctComma:
			field := strings.Trim(text(i+2), "'")
			if len(field)+2 != tokens[i+2].End-tokens[i+2].Start || !isFieldName(field) {
				return "", errUnrenderable
			}
			edits = append(edits, textEdit{tokens[i+2].Start, tokens[i+3].End, field + fromField})
		case text(i+1) == punctOpen && text(i+2) == punctClose:
			if _, ok := bareFunctions[word]; ok {
				edits = append(edits, textEdit{tokens[i+1].Start, tokens[i+2].End, ""})
			}
		}
	}
	if len(edits) == 0 {
		return rendered, nil
	}
	var out strings.Builder
	out.Grow(len(rendered) + 8*len(edits))
	at := 0
	for _, edit := range edits {
		out.WriteString(rendered[at:edit.start])
		out.WriteString(edit.text)
		at = edit.end
	}
	out.WriteString(rendered[at:])
	return out.String(), nil
}

func isFieldName(field string) bool {
	for i := range len(field) {
		if !isLetter(field[i]) && field[i] != '_' {
			return false
		}
	}
	return field != ""
}
