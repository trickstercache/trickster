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
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestPoolsCarryTheirAlignment(t *testing.T) {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	targets := Targets{NewTarget(http.NotFoundHandler(), st, nil)}
	want := Alignment{
		Mode: timeseries.StepAlignmentTruncate, Allowed: timeseries.StepAlignmentAll, Warning: "w",
	}
	aligned := NewAligned(targets, 0, want)
	defer aligned.Stop()
	plain := New(targets, 0)
	defer plain.Stop()
	if aligned.Alignment() != want || plain.Alignment() != (Alignment{}) {
		t.Fatalf("got %+v and %+v", aligned.Alignment(), plain.Alignment())
	}
	override := aligned.StepAlignmentOverride()
	if override == nil || *override != (tctx.StepAlignmentOverride{Mode: want.Mode, Allowed: want.Allowed}) ||
		plain.StepAlignmentOverride() != nil {
		t.Fatalf("overrides %+v and %+v", override, plain.StepAlignmentOverride())
	}
	// a pick reaches the alignment of the very pool it was made from
	bal := lb.NewBalancer(rr.New(), lb.BalancerOptions{Pool: aligned.Core()})
	pk, ok := bal.Pick(lb.Flow{})
	if !ok || OverrideOf(pk) != override {
		t.Fatalf("pick override: %v %v", ok, OverrideOf(pk))
	}
	// a core pool that no Pool built has none
	foreign, err := lb.NewPool([]*lb.Member{lb.NewMember(lb.MemberOptions{Name: "m"})}, 0,
		lb.PoolOptions{Value: "not a pool"})
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Stop()
	bal.SetPool(foreign)
	if pk, ok = bal.Pick(lb.Flow{}); !ok || OverrideOf(pk) != nil {
		t.Fatalf("foreign pick override: %v %v", ok, OverrideOf(pk))
	}
	if OverrideOf(lb.Pick{}) != nil {
		t.Fatal("the zero pick has no mode")
	}
}
