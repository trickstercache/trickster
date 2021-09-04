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
	"context"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing"
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

const tq01 = `/* this tests a multi-line comment at the front, where the query continues after` +
	`, and on the same line as, the comment closing delimiter */` +
	` SELECT test_table.field1 as f1,` +
	` count() as cnt FROM test_db.test_table WHERE datetime >= 1589904000 AND datetime < 1589997600` +
	` AND other_field BETWEEN 5 AND 10` +
	` GROUP BY t, apple` +
	` HAVING t > 10` +
	` ORDER BY  t DESC, s // test comment
// test 2 comment`

func TestParser(t *testing.T) {
	p := New(nil)
	if p == nil {
		t.Error("expected non-nil parser")
	}
	sp, ok := p.(*Parser)
	if !ok {
		t.Error("casting error")
	}
	if sp.Options() == nil {
		t.Error("expected non-nil options")
	}

	po := parsing.New(FindVerb, nil, nil)
	po.Decisions = map[string]parsing.DecisionSet{
		"FindVerb":            map[token.Typ]parsing.StateFn{token.Space: parsing.Noop, token.Bool: nil},
		"SelectQueryKeywords": map[token.Typ]parsing.StateFn{token.Space: parsing.Noop, token.Bool: nil}}
	sp = New(po).(*Parser)
	if sp == nil {
		t.Error("expected non-nil parser")
	}
	_, err := sp.Run(context.Background(), p, tq01)
	if err != parsing.ErrNoLexer {
		t.Error("expected error for no lexer")
	}

	lo := &lex.Options{}
	lexer := lsql.NewLexer(lo)
	po = parsing.New(FindVerb, lexer, lo)
	sp = New(po).(*Parser)
	if sp == nil {
		t.Error("expected non-nil parser")
	}
	_, err = sp.Run(context.Background(), sp, tq01+"\n LIMIT 10")
	if err != nil {
		t.Error(err)
	}

	_, err = sp.Run(context.Background(), sp, tq01+"\n UNION something else")
	if err != parsing.ErrUnsupportedKeyword {
		t.Error("expected error for UnsupportedKeyword", err)
	}

}

func TestUnsupportedVerb(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	UnsupportedVerb(nil, nil, rs)
	if rs.Error() != ErrUnsupportedVerb {
		t.Error("expected err for UnsupportedVerb")
	}
}

func TestUnsupportedClause(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	UnsupportedClause(nil, nil, rs)
	if rs.Error() != ErrUnsupportedClause {
		t.Error("expected err for UnsupportedClause")
	}
}

func TestFindVerbUnsupportedParser(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	FindVerb(nil, nil, rs)
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected err for UnsupportedParser")
	}
}
