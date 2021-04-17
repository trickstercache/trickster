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

package token

import (
	"errors"
	"strconv"
	"testing"
)

func TestClone(t *testing.T) {
	tk := &Token{
		Typ: Identifier,
		Pos: 42,
		Val: "trickster",
	}
	tk2 := tk.Clone()
	if tk2.Typ != tk.Typ ||
		tk2.Pos != tk.Pos ||
		tk2.Val != tk.Val {
		t.Error("clone mismatch")
	}
}

func TestInt64(t *testing.T) {
	tk := &Token{
		Typ: Identifier,
		Pos: 42,
		Val: "trickster",
	}
	i, err := tk.Int64()
	if err != ErrParsingInt {
		t.Error("expected ErrParsingInt")
	}
	if i != 0 {
		t.Error("expected 0")
	}
	tk.Typ = Number
	i, err = tk.Int64()
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Error("expected ErrParsingInt")
	}
	if i != 0 {
		t.Error("expected 0")
	}
	tk.Val = "42"
	i, err = tk.Int64()
	if err != nil {
		t.Error(err)
	}
	if i != 42 {
		t.Errorf("expected %d got %d", 42, i)
	}
}

var testToken = &Token{
	Typ: Identifier,
	Pos: 42,
	Val: "trickster",
}

func TestString(t *testing.T) {
	tk := testToken.Clone()
	s := tk.String()
	expected := "{type:ident,pos:42,val:`trickster`}"
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	tks := Tokens{}
	s = tks.String()
	expected = "[]"
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	tks = Tokens{tk, tk}
	s = tks.String()
	expected = "[{type:ident,pos:42,val:`trickster`},{type:ident,pos:42,val:`trickster`}]"
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestValues(t *testing.T) {
	tks := Tokens{testToken}
	v := tks.Values()
	if len(v) != 1 {
		t.Errorf("expected 1 got %d", len(v))
	}
	if v[0] != "trickster" {
		t.Error("values mismatch")
	}
}
