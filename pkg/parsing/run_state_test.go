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

package parsing

import (
	"context"
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

func TestNewRunState(t *testing.T) {
	rs := NewRunState(context.Background())
	if rs == nil {
		t.Error("expected non-nil error")
	}
}

func testStateFn(p1, p2 Parser, rs *RunState) StateFn {
	return nil
}

func TestRunState(t *testing.T) {
	v := "trickster"
	ctx := context.Background()
	rs := NewRunState(context.Background()).WithContext(ctx)
	rs.SetResultsCollection("test", v)
	v2, _ := rs.GetResultsCollection("test")
	if v3, ok := v2.(string); !ok || v3 != v {
		t.Errorf("expected %v got %v", v, v2)
	}
	if len(rs.Results()) != 1 {
		t.Errorf("expecgted %d got %d", 1, len(rs.results))
	}
	if rs.Context() != ctx {
		t.Error("context mismatch")
	}
	if rs.Previous() != nil {
		t.Error("expected nil prev")
	}
	if rs.Current() != nil {
		t.Error("expected nil curr")
	}
	if rs.IsPeeked() {
		t.Error("expected false")
	}
	err := errors.New("test")
	rs.err = nil
	rs = rs.WithError(err)
	if err != rs.Error() {
		t.Error("error mismatch")
	}
	if rs.GetReturnFunc(nil, nil, false) != nil {
		t.Error("expected nil return func")
	}
	rs.err = nil
	rs.nextOverride = nil
	rs = rs.WithNextOverride(testStateFn)
	if rs.nextOverride == nil {
		t.Error("nextOverride mismatch")
	}
	if rs.GetReturnFunc(nil, nil, false) == nil {
		t.Error("expected non-nil return func")
	}
	if rs.GetReturnFunc(testStateFn, nil, false) == nil {
		t.Error("expected non-nil return func")
	}

	tk := &token.Token{Typ: token.EOF}
	ch := rs.Tokens()
	ch <- tk
	rs.Peek()
	if rs.Next() != tk {
		t.Error("next mismatch")
	}
	if rs.Peek() != tk {
		t.Error("peek mismatch")
	}

	StateUnexpectedToken(nil, nil, rs)
	if rs.err != ErrUnexpectedToken {
		t.Error("expected err for unexpected token")
	}

	rs.err = nil
	rs.lastkw = &token.Token{Typ: token.Typ(100000)}
	rs.curr = &token.Token{Typ: token.CustomKeyword + 2}

	if rs.GetReturnFunc(testStateFn,
		DecisionSet{token.CustomKeyword + 2: testStateFn}, false) != nil {
		t.Error("expected nil return func")
	}

	rs.err = nil
	rs.lastkw = &token.Token{Typ: token.CustomKeyword + 1}
	rs.curr = &token.Token{Typ: token.CustomKeyword + 2}
	if rs.GetReturnFunc(testStateFn,
		DecisionSet{token.CustomKeyword + 2: testStateFn}, false) == nil {
		t.Error("expected non-nil return func")
	}
}
