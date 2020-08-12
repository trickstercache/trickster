/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"testing"

	"github.com/tricksterproxy/trickster/pkg/parsing"
	lsql "github.com/tricksterproxy/trickster/pkg/parsing/lex/sql"
	"github.com/tricksterproxy/trickster/pkg/parsing/token"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
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
