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

package lex

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

var testRunState = &RunState{
	Input:        "Test",
	InputLowered: "test",
	InputWidth:   4,
	Tokens:       make(chan *token.Token, 8),
}

func TestRunState(t *testing.T) {
	testRunState.InputWidth = 0
	r := testRunState.Next()
	if r != EOF {
		t.Errorf("expected EOF got %c", r)
	}
	testRunState.InputWidth = 4
	r = testRunState.Next()
	if r != 't' {
		t.Errorf("expected %c got %c", 't', r)
	}
	r = testRunState.Peek()
	if r != 'e' {
		t.Errorf("expected %c got %c", 'e', r)
	}
	testRunState.Start = 2
	testRunState.Pos = 3
	testRunState.Emit(token.Char)
	if testRunState.Start != 3 {
		t.Errorf("expected %d got %d", 3, testRunState.Start)
	}
	f := testRunState.EmitToken(&token.Token{})
	if f != nil {
		t.Error("expected nil state function")
	}
	testRunState.Start = 2
	testRunState.Pos = 3
	testRunState.Ignore()
	if testRunState.Start != 3 {
		t.Errorf("expected %d got %d", 3, testRunState.Start)
	}
	b := testRunState.Accept("test")
	if !b {
		t.Error("expected true")
	}
	b = testRunState.Accept("no")
	if b {
		t.Error("expected false")
	}
	testRunState.Start = 2
	testRunState.Pos = 3
	b = testRunState.AcceptRun("test")
	if !b {
		t.Error("expected true")
	}
	testRunState.Start = 2
	testRunState.Pos = 3
	b = testRunState.AcceptRun("no")
	if b {
		t.Error("expected false")
	}
}

func TestScanNumber(t *testing.T) {

	tr := &RunState{
		Input:        "1234",
		InputLowered: "1234",
		InputWidth:   4,
		Tokens:       make(chan *token.Token, 8),
	}
	b := tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "0x01"
	tr.InputLowered = "0x01"
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "0o01"
	tr.InputLowered = "0o01"
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "0b01"
	tr.InputLowered = "0b01"
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "0.01"
	tr.InputLowered = "0.01"
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "10e9"
	tr.InputLowered = "10e9"
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "0x10p8"
	tr.InputLowered = "0x10p8"
	tr.InputWidth = 6
	b = tr.ScanNumber()
	if !b {
		t.Error("expected true")
	}

	tr.Pos = 0
	tr.Start = 0
	tr.Input = "57ix"
	tr.InputLowered = "57ix"
	tr.InputWidth = 4
	b = tr.ScanNumber()
	if b {
		t.Error("expected false")
	}

}

func TestAtTeminator(t *testing.T) {
	tr := &RunState{
		Input:        " ",
		InputLowered: " ",
		InputWidth:   1,
		Tokens:       make(chan *token.Token, 8),
	}
	if !tr.AtTerminator() {
		t.Error("expected true")
	}
}

func TestErrorf(t *testing.T) {
	tr := &RunState{
		Input:        " ",
		InputLowered: " ",
		InputWidth:   1,
		Tokens:       make(chan *token.Token, 8),
	}
	tk := tr.Errorf("%s", "fail")
	if tk.Val != "fail" {
		t.Errorf("error mismatch")
	}
}
