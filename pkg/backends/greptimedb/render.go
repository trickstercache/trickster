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

package greptimedb

import (
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

var errUnrenderable = errors.New("statement has no faithful GreptimeDB re-spelling")

type textEdit struct {
	start, end int
	text       string
}

func postRender(rendered string) (string, error) {
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
		switch text(i) {
		case "STRING":
			edits = append(edits, textEdit{token.Start, token.End, "TEXT"})
		case "BYTES":
			edits = append(edits, textEdit{token.Start, token.End, "BYTEA"})
		case "bucket":
			// DataFusion reserves this word, but Cockroach drops its identifier quotes.
			edits = append(edits, textEdit{token.Start, token.End, `"bucket"`})
		case "extract":
			if text(i+1) != "(" || i+3 >= len(tokens) || tokens[i+2].Kind != sqlscan.String || text(i+3) != "," {
				continue
			}
			field := strings.Trim(text(i+2), "'")
			if field == "" || len(field)+2 != tokens[i+2].End-tokens[i+2].Start {
				return "", errUnrenderable
			}
			for _, char := range field {
				if char != '_' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
					return "", errUnrenderable
				}
			}
			edits = append(edits, textEdit{tokens[i+2].Start, tokens[i+3].End, field + " FROM"})
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
