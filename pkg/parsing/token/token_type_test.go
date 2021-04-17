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

import "testing"

func TestTypString(t *testing.T) {
	if testToken.Typ.String() != "ident" {
		t.Error("type mismatch")
	}
	t2 := testToken.Clone()
	t2.Typ = Typ(12812123412)
	if t2.Typ.String() != "12812123412" {
		t.Error("type mismatch")
	}
}

func TestIsBreakable(t *testing.T) {
	if testToken.Typ.IsBoolean() {
		t.Error("expected false")
	}
	if !EOF.IsBreakable() {
		t.Error("expected true")
	}
}

func TestIsGreaterOrLessThan(t *testing.T) {
	if testToken.Typ.IsGreaterOrLessThan() {
		t.Error("expected false")
	}
	if !GreaterThan.IsGreaterOrLessThan() {
		t.Error("expected true")
	}
	if testToken.Typ.IsGTorGTE() {
		t.Error("expected false")
	}
	if !GreaterThan.IsGTorGTE() {
		t.Error("expected true")
	}
	if testToken.Typ.IsLTorLTE() {
		t.Error("expected false")
	}
	if !LessThan.IsLTorLTE() {
		t.Error("expected true")
	}
	if testToken.Typ.IsOrEquals() {
		t.Error("expected false")
	}
	if !LessThanOrEqual.IsOrEquals() {
		t.Error("expected true")
	}
}

func TestIsMathOp(t *testing.T) {
	if testToken.Typ.IsMathOperator() {
		t.Error("expected false")
	}
	if !Plus.IsMathOperator() {
		t.Error("expected true")
	}
	if testToken.Typ.IsAddOrSubtract() {
		t.Error("expected false")
	}
	if !Plus.IsAddOrSubtract() {
		t.Error("expected true")
	}
}

func TestIsErr(t *testing.T) {
	if testToken.Typ.IsErr() {
		t.Error("expected false")
	}
	if !Error.IsErr() {
		t.Error("expected true")
	}
}

func TestIsEOF(t *testing.T) {
	if testToken.Typ.IsEOF() {
		t.Error("expected false")
	}
	if !EOF.IsEOF() {
		t.Error("expected true")
	}
}

func TestIsSpaceChar(t *testing.T) {
	if testToken.Typ.IsSpaceChar() {
		t.Error("expected false")
	}
	if !Space.IsSpaceChar() {
		t.Error("expected true")
	}
}

func TestIsLogicalOperator(t *testing.T) {
	if IsLogicalOperator(testToken.Typ) {
		t.Error("expected false")
	}
	if !IsLogicalOperator(LogicalAnd) {
		t.Error("expected true")
	}
}

func TestIsComma(t *testing.T) {
	if IsComma(testToken.Typ) {
		t.Error("expected false")
	}
	if !IsComma(Comma) {
		t.Error("expected true")
	}
}
