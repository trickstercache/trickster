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

package sqlparser

import (
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestNew(t *testing.T) {
	p := New(&parsing.Options{})
	if p == nil {
		t.Errorf("expected non-nil parser")
	}
}

func TestRunContext(t *testing.T) {

	trq := &timeseries.TimeRangeQuery{Statement: "trickster"}
	ro := &timeseries.RequestOptions{TimeFormat: 42}

	rc := NewRunContext(trq, ro)
	if rc == nil {
		t.Errorf("expected non-nil rc")
	}

	t2, r2 := ArtifactsFromContext(rc)
	if t2 == nil {
		t.Errorf("expected non-nil trq")
	}
	if r2 == nil {
		t.Errorf("expected non-nil ro")
	}
	if t2.Statement != "trickster" {
		t.Errorf("run context persistence error")
	}
	if r2.TimeFormat != 42 {
		t.Errorf("run context persistence error")
	}

}

func TestParseComment(t *testing.T) {

	trq := &timeseries.TimeRangeQuery{Statement: "trickster"}
	ro := &timeseries.RequestOptions{TimeFormat: 42}
	rc := NewRunContext(trq, ro)
	rs := parsing.NewRunState(rc)
	ch := rs.Tokens()
	ch <- &token.Token{Typ: lsql.TokenComment, Val: ":)"}
	rs.Next()
	ParseFVComment(nil, nil, rs)
	if rs.Current().Typ != lsql.TokenComment {
		t.Error("token parsing error")
	}

}

func TestParseEpoch(t *testing.T) {

	tests := []struct {
		input string
		exp1  epoch.Epoch
		exp2  byte
		exp3  error
	}{
		{
			input: "1577836800",
			exp1:  epoch.Epoch(1577836800) * billion,
			exp2:  0,
		},
		{
			input: "1577836800000",
			exp1:  epoch.Epoch(1577836800000) * million,
			exp2:  1,
		},
		{
			input: "2020-01-01",
			exp1:  epoch.Epoch(1577836800000) * million,
			exp2:  3,
		},
		{
			input: "2020-01-01 00:00:00",
			exp1:  epoch.Epoch(1577836800000) * million,
			exp2:  2,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			out, typ, err := ParseEpoch(test.input)
			if err != test.exp3 {
				t.Error(err)
			}
			if typ != test.exp2 {
				t.Errorf("got %d expected %d", typ, test.exp2)
			}
			if out != test.exp1 {
				t.Errorf("got %d expected %d", out, test.exp1)
			}
		})
	}

}

func TestFormatOutputTime(t *testing.T) {

	tests := []struct {
		input epoch.Epoch
		exp1  string
		typ   byte
		exp3  error
	}{
		{
			input: epoch.Epoch(1577836800) * billion,
			exp1:  "1577836800",
			typ:   0,
		},
		{
			input: epoch.Epoch(1577836800000) * million,
			exp1:  "1577836800000",
			typ:   1,
		},
		{
			input: epoch.Epoch(1577836800000) * million,
			exp1:  "2020-01-01",
			typ:   3,
		},
		{
			input: epoch.Epoch(1577836800000) * million,
			exp1:  "2020-01-01 00:00:00",
			typ:   2,
		},
		{
			input: epoch.Epoch(0) * million,
			exp1:  "0",
			typ:   8,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			out := FormatOutputTime(test.input, test.typ)
			if out != test.exp1 {
				t.Errorf("got %s expected %s", out, test.exp1)
			}
		})
	}

}
