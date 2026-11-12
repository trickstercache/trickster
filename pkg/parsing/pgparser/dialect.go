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

package pgparser

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
)

// DialectSpec describes the syntax an engine adds to the PostgreSQL-family grammar.
type DialectSpec struct {
	// Clauses are the keyword phrases, such as "SAMPLE BY", that open a clause after a query's WINDOW
	// clause. A clause runs to the next WHERE, GROUP, HAVING, WINDOW, ORDER, LIMIT, OFFSET, FETCH,
	// FOR, set operator or dialect clause outside parentheses, or to the end of its query, and parses
	// into an ast.DialectClause kept as written. A word may be punctuation, as in "INTERVAL (".
	Clauses []string
	// FunctionSuffixes are keywords, such as RANGE, that may follow a function call with a string
	// constant and then any of SuffixOptions, each with one value, as in RANGE '5m' FILL NULL.
	FunctionSuffixes []string
	// SuffixOptions are the keywords that may follow a function suffix's string constant.
	SuffixOptions []string
	// DurationLiterals scans an integer followed directly by letters, such as 5m, as one literal.
	DurationLiterals bool
	// BacktickIdentifiers reads backtick-quoted text as an identifier, as double-quoted text is.
	BacktickIdentifiers bool
}

// Dialect is a compiled DialectSpec. Build it once with NewDialect; it is safe for concurrent use.
type Dialect struct {
	clauses       []clauseSpec
	suffixes      []string
	suffixOptions []string
	durations     bool
	backticks     bool
}

type clauseSpec struct {
	words   []string
	keyword string
}

// NewDialect compiles spec.
func NewDialect(spec DialectSpec) *Dialect {
	d := &Dialect{
		suffixes:      lowerWords(spec.FunctionSuffixes),
		suffixOptions: lowerWords(spec.SuffixOptions),
		durations:     spec.DurationLiterals,
		backticks:     spec.BacktickIdentifiers,
	}
	for _, phrase := range spec.Clauses {
		if words := lowerWords(strings.Fields(phrase)); len(words) > 0 {
			d.clauses = append(d.clauses, clauseSpec{words: words, keyword: strings.Join(words, " ")})
		}
	}
	return d
}

func lowerWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, word := range words {
		if word = strings.ToLower(strings.TrimSpace(word)); word != "" {
			out = append(out, word)
		}
	}
	return out
}

func (d *Dialect) rewrites() bool {
	return d != nil && (len(d.clauses) > 0 || len(d.suffixes) > 0)
}

func (d *Dialect) collapse(sql string, tokens []sqlSymType) ([]sqlSymType, bool) {
	w, changed := 0, false
	for i := 0; i < len(tokens); {
		id := int32(DIALECT_CLAUSE)
		end, node := d.clauseSpan(sql, tokens, i)
		if node == nil && w > 0 && tokens[w-1].id == ')' {
			id = DIALECT_SUFFIX
			end, node = d.suffixSpan(sql, tokens, i)
		}
		if node == nil {
			tokens[w] = tokens[i]
			w, i = w+1, i+1
			continue
		}
		pos := tokens[i].pos
		tokens[w] = sqlSymType{
			id: id, pos: pos, end: pos + int32(len(node.Text)), // #nosec G115 -- bounded by the statement
			str: node.Text, union: sqlSymUnion{val: node},
		}
		w, i, changed = w+1, end, true
	}
	return tokens[:w], changed
}

func tokenIs(sql string, tok *sqlSymType, word string) bool {
	// a quoted name or a string never matches: its written text holds its quotes
	return int(tok.end-tok.pos) == len(word) && strings.EqualFold(sql[tok.pos:tok.end], word)
}

func (d *Dialect) opensClause(sql string, tokens []sqlSymType, i int) (*clauseSpec, bool) {
	for k := range d.clauses {
		spec := &d.clauses[k]
		if i+len(spec.words) > len(tokens) {
			continue
		}
		matched := true
		for j, word := range spec.words {
			if !tokenIs(sql, &tokens[i+j], word) {
				matched = false
				break
			}
		}
		if matched {
			return spec, true
		}
	}
	return nil, false
}

func (d *Dialect) clauseSpan(sql string, tokens []sqlSymType, i int) (int, *ast.DialectClause) {
	spec, ok := d.opensClause(sql, tokens, i)
	if !ok {
		return 0, nil
	}
	head := i + len(spec.words)
	end := head
	// a phrase may open a parenthesis, as "INTERVAL (" does; the clause holds its close
	depth := 0
	for _, tok := range tokens[i:head] {
		switch tok.id {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		}
	}
	for ; end < len(tokens); end++ {
		tok := &tokens[end]
		switch tok.id {
		case ERROR:
			return 0, nil
		case '(', '[':
			depth++
			continue
		case ')', ']':
			if depth == 0 {
				return clauseNode(sql, tokens, i, head, end, spec.keyword)
			}
			depth--
			continue
		}
		if depth > 0 {
			continue
		}
		if endsDialectClause(tok.id) {
			break
		}
		if _, next := d.opensClause(sql, tokens, end); next {
			break
		}
	}
	return clauseNode(sql, tokens, i, head, end, spec.keyword)
}

func endsDialectClause(id int32) bool {
	switch id {
	case WHERE, GROUP, HAVING, WINDOW, ORDER, LIMIT, OFFSET, FETCH, FOR, UNION, INTERSECT, EXCEPT:
		return true
	}
	return false
}

func clauseNode(sql string, tokens []sqlSymType, start, head, end int, keyword string) (int, *ast.DialectClause) {
	last := tokens[end-1].end
	node := &ast.DialectClause{
		Keyword: keyword,
		Text:    strings.TrimRight(sql[tokens[start].pos:last], spaceChars),
	}
	if end > head {
		node.Body = strings.TrimRight(sql[tokens[head].pos:last], spaceChars)
	}
	return end, node
}

func (d *Dialect) suffixSpan(sql string, tokens []sqlSymType, i int) (int, *ast.DialectClause) {
	for _, keyword := range d.suffixes {
		if !tokenIs(sql, &tokens[i], keyword) {
			continue
		}
		end := i + 1
		if end >= len(tokens) || tokens[end].id != SCONST {
			return 0, nil
		}
		end++
		// a window frame's RANGE takes an offset too, and is followed by PRECEDING or FOLLOWING
		if end < len(tokens) && (tokens[end].id == PRECEDING || tokens[end].id == FOLLOWING) {
			return 0, nil
		}
		for end < len(tokens) && d.isSuffixOption(sql, &tokens[end]) {
			end++
			if end < len(tokens) && (tokens[end].id == '-' || tokens[end].id == '+') {
				end++
			}
			if end >= len(tokens) || !isSuffixValue(tokens[end].id) {
				return 0, nil
			}
			end++
		}
		return clauseNode(sql, tokens, i, i+1, end, keyword)
	}
	return 0, nil
}

func isSuffixValue(id int32) bool {
	switch id {
	case IDENT, SCONST, ICONST, FCONST, NULL:
		return true
	}
	return false
}

func (d *Dialect) isSuffixOption(sql string, tok *sqlSymType) bool {
	for _, option := range d.suffixOptions {
		if tokenIs(sql, tok, option) {
			return true
		}
	}
	return false
}
