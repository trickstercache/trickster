/*
 * Copyright 2026 The Trickster Authors
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
package pick

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// firstMember is a strategy with a need, so dispatch through it is accounted for
type firstMember struct{ members []*lb.Member }

func (*firstMember) Name() string { return "first" }

func (*firstMember) Needs() lb.Needs { return lb.NeedInflight }

func (*firstMember) Prepare(s *lb.Snapshot) lb.Prepared { return &firstMember{members: s.Members} }

func (f *firstMember) Select(lb.Flow) *lb.Member { return f.members[0] }

var _ types.PickerMechanism = (*handler)(nil)

func TestPickerAndName(t *testing.T) {
	h := New("first", &firstMember{}).(*handler)
	if h.Name() != "first" {
		t.Errorf("name = %q", h.Name())
	}
	if h.Balancer() == nil || h.Picker() != lb.Picker(h.Balancer()) {
		t.Error("the picker is not the mechanism's balancer")
	}
	if h.Picker() == nil || h.Picker().Needs() != lb.NeedInflight {
		t.Error("the mechanism does not expose its balancer")
	}
	if _, ok := h.Picker().Pick(lb.Flow{}); ok {
		t.Error("picked before a pool was installed")
	}
	p, _, _ := albpool.NewHealthy([]http.Handler{http.NotFoundHandler()})
	defer p.Stop()
	h.SetPool(p)
	if h.Pool() != p {
		t.Error("the pool was not held")
	}
	pk, ok := h.Picker().Pick(lb.Flow{})
	if !ok {
		t.Fatal("no pick from an installed pool")
	}
	pk.Done(lb.OutcomeOK)
	// removing the pool leaves nothing to pick from, and a 502 to serve
	h.SetPool(nil)
	if _, ok := h.Picker().Pick(lb.Flow{}); ok {
		t.Error("picked after the pool was removed")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 with no pool, got %d", w.Code)
	}
	h.StopPool()
}

// a strategy that tracks in-flight work sees the request while it runs and not after, even
// when the member's handler panics
func TestTrackedDispatchBalancesInflight(t *testing.T) {
	var during int64
	var tgt *pool.Target
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = tgt.Member().Stats().Inflight()
		if r.URL.Path == "/panic" {
			panic("member blew up")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p, targets, _ := albpool.NewHealthy([]http.Handler{serve})
	defer p.Stop()
	tgt = targets[0]
	h := New("first", &firstMember{})
	h.SetPool(p)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if w.Code != http.StatusNoContent || during != 1 {
		t.Errorf("code %d, in-flight during the request = %d", w.Code, during)
	}
	if got := tgt.Member().Stats().Inflight(); got != 0 {
		t.Errorf("in-flight after the request = %d", got)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the member's panic was swallowed")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil))
	}()
	if got := tgt.Member().Stats().Inflight(); got != 0 {
		t.Errorf("a panicking member leaked %d in flight", got)
	}
}

// a member whose payload is not a dispatchable target is a 502, with its pick accounted for
func TestUndispatchableMemberIsBadGateway(t *testing.T) {
	stray := lb.NewMember(lb.MemberOptions{Name: "stray", Value: "not a target"})
	core, err := lb.NewPool([]*lb.Member{stray}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	h := New("first", &firstMember{}).(*handler)
	h.balancer.SetPool(core)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", w.Code)
	}
	if got := stray.Stats().Inflight(); got != 0 {
		t.Errorf("the refused pick leaked %d in flight", got)
	}
}
