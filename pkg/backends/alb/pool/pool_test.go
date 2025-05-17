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

func TestNewTarget(t *testing.T) {
	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}
}

func TestNewPool(t *testing.T) {

	s := &healthcheck.Status{}
	tgt := NewTarget(http.NotFoundHandler(), s)
	if tgt.hcStatus != s {
		t.Error("unexpected mismatch")
	}

	p := New([]*Target{tgt}, 1)
	if p == nil {
		t.Error("expected non-nil")
	}

	if len(p.Healthy()) != 0 {
		t.Error("expected 0 healthy target", len(p.Healthy()))
	}

	p.Stop()

	p2 := p.(*pool)
	hl := []http.Handler{http.NotFoundHandler()}
	p2.healthy.Store(&hl)

	if len(p.Healthy()) != 1 {
		t.Error("expected 1 healthy target", len(p.Healthy()))
	}

}
