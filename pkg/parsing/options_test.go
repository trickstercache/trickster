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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

func TestOptions(t *testing.T) {

	o := New(nil, nil, nil)
	if o == nil {
		t.Error("expected non-nil options")
	}
	d := o.GetDecisions("test")
	if d != nil {
		t.Error("expected nil decisions")
	}
	o = o.WithDecisions("test", DecisionSet{token.Bool: testStateFn})
	if o == nil {
		t.Error("expected non-nil options")
	}
	d = o.GetDecisions("test")
	if d == nil {
		t.Error("expected non-nil decisions")
	}
	d = o.GetDecisions("test2")
	if d != nil {
		t.Error("expected nil decisions")
	}
	o = o.WithEntryFunc(testStateFn)
	if o == nil {
		t.Error("expected non-nil options")
	}
	if o.EntryFunc() == nil {
		t.Error("expected non-nil entryFunc")
	}
	o = o.WithLexer(nil, nil)
	if o == nil {
		t.Error("expected non-nil options")
	}

	l, lo := o.Lexer()
	if l != nil {
		t.Error("expected nil lexer")
	}
	if lo != nil {
		t.Error("expected nil lexopts")
	}

}
