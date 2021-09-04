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

package sql

import (
	"fmt"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

type mockParser struct {
	ch  chan *token.Token
	l   lex.Lexer
	lo  *lex.Options
	err string
	wg  sync.WaitGroup
}

// this mock parser simply drains the channel to prevent the
// lexer from blocking on a full channel
func (p *mockParser) run() {
	var t *token.Token
	for ; ; t = <-p.ch {
		if t != nil && t.Typ == token.Error {
			p.err = t.Val
		}
		if t != nil && (t.Typ == token.EOF ||
			t.Typ == token.Error) {
			break
		}
	}
	p.wg.Done()
}

func newLexTestHarness(lo *lex.Options) *mockParser {
	if lo == nil {
		lo = &lex.Options{}
	}
	return &mockParser{ch: make(chan *token.Token, 256), lo: lo, l: NewLexer(lo)}
}

func TestLexer(t *testing.T) {

	defaultLO := &lex.Options{CustomKeywords: map[string]token.Typ{"test": TokenWhere},
		SpacedKeywordHints: SpacedKeywords()}

	tests := []struct {
		in, expected string
		lo           *lex.Options
	}{
		{
			in:       "/* Comment",
			lo:       defaultLO,
			expected: "unclosed comment",
		},
		{
			in:       "/* Comment */",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test //EOL COMMENT\r\nx",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test //EOL COMMENT",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test (unexpected right paren ))",
			lo:       defaultLO,
			expected: "unexpected right paren",
		},
		{
			in:       "test high ascii ç",
			lo:       defaultLO,
			expected: "unrecognized character in action: U+00E7 'ç'",
		},
		{
			in:       "test other symbols @ % ^",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test booleans true false",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test spaced keyword GROUP BY x",
			lo:       defaultLO,
			expected: "",
		},
		{
			in:       "test invalid number 92z3",
			lo:       defaultLO,
			expected: `bad number syntax: "92z"`,
		},
		{
			in:       "test complex number 1+2i",
			lo:       defaultLO,
			expected: ``,
		},
		{
			in:       "test complex number 1+2x",
			lo:       defaultLO,
			expected: `bad number syntax: "1+2x"`,
		},
		{
			in:       "test 'unterminated quoted string",
			lo:       defaultLO,
			expected: `unterminated quoted string`,
		},
		{
			in:       "test 'quoted string with '' embedded quote'",
			lo:       defaultLO,
			expected: ``,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			p := newLexTestHarness(test.lo)
			p.wg.Add(1)
			go p.run()
			p.l.Run(test.in, p.ch)
			p.wg.Wait()
			if p.err != test.expected {
				t.Errorf(`expected "%v" got "%v"`, test.expected, p.err)
			}
		},
		)
	}
}

func TestStateFuncs(t *testing.T) {
	tests := []struct {
		f, expected lex.StateFn
	}{
		{
			emitEOF, nil,
		},
		{
			emitComma, lexText,
		},
		{
			emitAsterisk, lexText,
		},
		{
			emitEqual, lexText,
		},
		{
			emitPlus, lexText,
		},
		{
			emitMinus, lexText,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			p := newLexTestHarness(nil)
			rs := &lex.RunState{Tokens: p.ch}
			f := test.f(p.l, rs)
			if (test.expected != nil && f == nil) ||
				(test.expected == nil && f != nil) {
				t.Error("unexpected return func")
			}
		},
		)
	}
}

func TestLexIdentifierError(t *testing.T) {
	expected := `bad character U+0040 '@'`
	p := newLexTestHarness(nil)
	p.wg.Add(1)
	go p.run()
	rs := &lex.RunState{Tokens: p.ch, Pos: 2, InputLowered: "@@@@@@", InputWidth: 6}
	f := lexIdentifier
	for f != nil {
		f = f(p.l, rs)
	}
	p.wg.Wait()
	if p.err != expected {
		t.Errorf("expected `%s` got `%s`", expected, p.err)
	}
}

func TestFullSQLLex(t *testing.T) {
	p := newLexTestHarness(nil)
	p.wg.Add(1)
	go p.run()
	stmt := `SELECT t1.x as x, t2.count(*) as cnt FROM test_db.test_table t1
	INNER JOIN test_db.test_table2 t2 ON
	t1.id =  t2.secondary_id
	WHERE (t2.datetime >= 1 /* ENCLOSED COMMENT */
	/* ENCLOSED MULTI-
	LINE COMMENT */
	AND t2.datetime <= 10 / 2
	AND t2.datetime != 5)
	OR
	(x = 'my \'Really Awesome\' single quote coverage string' and x != '')
	GROUP BY x, cnt
	HAVING cnt > 15 // EOL COMMENT
	ORDER BY x
	LIMIT 100 // EOL COMMENT`
	p.l.Run(stmt, p.ch)
	p.wg.Wait()
	if p.err != "" {
		t.Error(p.err)
	}
}

func TestUnQuote(t *testing.T) {
	tests := []struct {
		in, expected string
	}{
		{
			`'this quoted string wouldn\'t be the same without the contraction'`,
			`this quoted string wouldn\'t be the same without the contraction`,
		},
		{
			"''", "",
		},
		{
			"", "",
		},
	}
	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			out := UnQuote(test.in)
			if out != test.expected {
				t.Errorf(`expected "%s" got "%s"`, test.expected, out)
			}
		},
		)
	}
}

func TestSpacedKeywords(t *testing.T) {
	sk := SpacedKeywords()
	if sk == nil {
		t.Errorf("expected non-nil map")
	}
	if i, ok := sk["group"]; !ok || i != 3 {
		t.Errorf(`expected "group":3`)
	}
}
