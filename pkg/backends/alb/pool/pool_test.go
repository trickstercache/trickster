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

package pool

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func TestMechsToFuncs(t *testing.T) {

	m := mechsToFuncs()
	if len(m) != 5 {
		t.Errorf("expected %d got %d", 5, len(m))
	}

	if _, ok := m[RoundRobin]; !ok {
		t.Error("expected true")
	}

}

func TestNext(t *testing.T) {

	p := &pool{f: testNextFunc}
	l := p.Next()
	if len(l) != 1 {
		t.Errorf("expected %d got %d", 1, len(l))
	}

}

func testNextFunc(p *pool) []http.Handler {
	return []http.Handler{http.NotFoundHandler()}
}

func TestNewTarget(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}
}

func TestNewPool(t *testing.T) {

	p := New(83, nil, 0)
	if p != nil {
		t.Error("expected nil pool")
	}

	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}

	p = New(RoundRobin, []*Target{tgt}, 0)
	if p == nil {
		t.Error("expected non-nil")
	}

}
