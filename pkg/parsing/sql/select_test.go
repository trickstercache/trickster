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
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

func TestSelectTokens(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	ts := SelectTokens(rs)
	if ts != nil {
		t.Error("expected nil tokens list")
	}
	rs.SetResultsCollection("selectTokens", true)
	ts = SelectTokens(rs)
	if ts != nil {
		t.Error("expected nil tokens list")
	}
	rs.SetResultsCollection("selectTokens", []token.Tokens{})
	ts = SelectTokens(rs)
	if ts == nil {
		t.Error("expected non-nil tokens list")
	}
}

func TestSelectQueryKeywords(t *testing.T) {
	v := New(nil).(*Parser).SelectQueryKeywords()
	if v == nil {
		t.Error("expected non-nil list")
	}
}

func TestDefaultIsBreakable(t *testing.T) {
	if !DefaultIsBreakable(lsql.TokenFrom) {
		t.Error("expected true")
	}
}

func TestIsWhereBreakable(t *testing.T) {
	if !IsWhereBreakable(lsql.TokenFrom) {
		t.Error("expected true")
	}
}

func TestDefaultIsContinuable(t *testing.T) {
	if !DefaultIsContinuable(token.Space) {
		t.Error("expected true")
	}
}

func TestIsFromFieldDelimiterType(t *testing.T) {
	if !IsFromFieldDelimiterType(token.Comma) {
		t.Error("expected true")
	}
}

func TestHasLimitClause(t *testing.T) {
	if HasLimitClause(map[string]interface{}{}) {
		t.Error("expected false")
	}
}

func TestGetFieldList(t *testing.T) {
	p := New(nil).(*Parser)
	rs := parsing.NewRunState(context.Background())
	ch := rs.Tokens()
	ch <- &token.Token{Typ: token.Space, Pos: 0, Val: " "}
	rs.Next()
	p.GetFieldList(rs, token.Bool, parsing.ErrUnexpectedToken, nil, nil, nil, false)
	if rs.Error() != parsing.ErrUnexpectedToken {
		t.Error("expected ErrUnexpectedToken")
	}
}

func TestAtSelect(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtSelect(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}

	p := New(nil)
	rs = parsing.NewRunState(context.Background())
	ch := rs.Tokens()
	ch <- &token.Token{Typ: token.Space, Pos: 0, Val: " "}
	rs.Next()
	f = AtSelect(p, p, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != ErrNotAtSelect {
		t.Error("expected ErrNotAtSelect")
	}

	rs = parsing.NewRunState(context.Background())
	ch = rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenSelect, Pos: 0, Val: "SELECT"}
	ch <- &token.Token{Typ: token.Identifier, Pos: 7, Val: "x"}
	ch <- &token.Token{Typ: token.EOF, Pos: 8, Val: ""}
	rs.Next()
	f = AtSelect(p, p, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != ErrNotAtFrom {
		t.Error("expected ErrNotAtFrom")
	}
}

func TestAtFrom(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtFrom(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtWhere(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtWhere(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtGroupBy(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtGroupBy(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}

	p := New(nil)
	rs = parsing.NewRunState(context.Background())
	ch := rs.Tokens()
	ch <- &token.Token{Typ: token.Space, Pos: 0, Val: " "}
	rs.Next()
	f = AtGroupBy(p, p, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != ErrNotAtGroupBy {
		t.Error("expected ErrNotAtGroupBy")
	}
}

func TestAtHaving(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtHaving(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtOrderBy(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtOrderBy(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtLimit(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtLimit(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedParser {
		t.Error("expected ErrUnsupportedParser")
	}
}

func TestAtIntoOutfile(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtIntoOutfile(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedKeyword {
		t.Error("expected ErrUnsupportedKeyword")
	}
}

func TestAtUnion(t *testing.T) {
	rs := parsing.NewRunState(context.Background())
	f := AtUnion(nil, nil, rs)
	if f != nil {
		t.Error("expected nil StateFn")
	}
	if rs.Error() != parsing.ErrUnsupportedKeyword {
		t.Error("expected ErrUnsupportedKeyword")
	}
}
