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
package observe

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPoolObserverMetersRecoveredPanics(t *testing.T) {
	c := metrics.ALBPoolRefreshPanicRecovered.WithLabelValues(WorkerRefresh)
	before := testutil.ToFloat64(c)
	o := Pool()
	o.Observe(lb.Event{Kind: lb.EventSnapshot, Gen: 1})
	if got := testutil.ToFloat64(c) - before; got != 0 {
		t.Errorf("a snapshot event was metered as a panic: +%v", got)
	}
	o.Observe(lb.Event{Kind: lb.EventPanic, Panic: "boom", Stack: []byte("stack")})
	if got := testutil.ToFloat64(c) - before; got != 1 {
		t.Errorf("recovered panics metered = +%v, want +1", got)
	}
}

func TestBalancerObserverMetersEjections(t *testing.T) {
	c := metrics.ALBMemberEjections.WithLabelValues("observed-alb", "observed-member")
	before := testutil.ToFloat64(c)
	o := Balancer("observed-alb")
	o.Observe(lb.Event{Kind: lb.EventSnapshot})
	o.Observe(lb.Event{Kind: lb.EventPanic, Panic: "boom"})
	if got := testutil.ToFloat64(c) - before; got != 0 {
		t.Errorf("an event that is not an ejection was metered as one: +%v", got)
	}
	o.Observe(lb.Event{Kind: lb.EventEjected, Member: "observed-member"})
	if got := testutil.ToFloat64(c) - before; got != 1 {
		t.Errorf("ejections metered = +%v, want +1", got)
	}
	// a member that leaves takes its series with it
	metrics.DeleteBackendSeries("observed-member")
	if got := testutil.ToFloat64(metrics.ALBMemberEjections.WithLabelValues("observed-alb", "observed-member")); got != 0 {
		t.Errorf("the series survived its member: %v", got)
	}
}
