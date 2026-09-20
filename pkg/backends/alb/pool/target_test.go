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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func testBackend(t *testing.T, name string, o *bo.Options) backends.Backend {
	t.Helper()
	if err := o.Initialize(name); err != nil {
		t.Fatal(err)
	}
	b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// balanced is a backend that is itself a load balancer
type balanced struct {
	backends.Backend
	picker lb.Picker
}

func (b balanced) Picker() lb.Picker { return b.picker }

func TestTargetDescribesItsBackend(t *testing.T) {
	o := bo.New()
	o.OriginURL = "tcp://10.0.0.1:5432"
	b := testBackend(t, "pg1", o)
	st := &healthcheck.Status{}
	h := http.NotFoundHandler()
	tgt := NewWeightedTarget(h, st, b, 0)
	if tgt.Name() != "pg1" || tgt.ReplicaGroup() != "pg1" || tgt.Weight() != 1 {
		t.Errorf("identity = %q %q %d", tgt.Name(), tgt.ReplicaGroup(), tgt.Weight())
	}
	if tgt.Addr() != "10.0.0.1:5432" {
		t.Errorf("addr = %q", tgt.Addr())
	}
	if tgt.Backend() != b || tgt.HealthStatus() != st || tgt.Handler() == nil {
		t.Error("the target lost its backend, status or handler")
	}
	m := tgt.Member()
	if m.Name() != "pg1" || m.Group() != "pg1" || m.Weight() != 1 || m.Value != tgt {
		t.Errorf("member = %+v", m)
	}
	if !tgt.Dialable() || tgt.Picker() != nil {
		t.Error("an origin with an address is dialable, and is not itself balanced")
	}
	// a member under the reserved .invalid domain, or with no address, can never be dialed
	gone := bo.New()
	gone.OriginURL = "tcp://unresolved.kgw.invalid:1"
	if NewTarget(h, st, testBackend(t, "gone", gone)).Dialable() {
		t.Error("a member under .invalid reports as dialable")
	}
	if NewTarget(h, st, testBackend(t, "hostless", bo.New())).Dialable() || NewTarget(h, st, nil).Dialable() {
		t.Error("a member with no address reports as dialable")
	}
	// a member that is itself balanced offers its picker, or nil when it has none to offer
	picker := lb.NewBalancer(rr.New())
	if got := NewTarget(h, st, balanced{Backend: b, picker: picker}).Picker(); got != lb.Picker(picker) {
		t.Errorf("nested picker = %v", got)
	}
	if NewTarget(h, st, balanced{Backend: b}).Picker() != nil {
		t.Error("a balanced member with no picker offered one")
	}
	// with no health check interval the status can never leave Unchecked
	if tgt.Probed() {
		t.Error("a member without a health check interval reports as probed")
	}
	if !tgt.WithExternalHealth().Probed() {
		t.Error("an externally driven member reports as unprobed")
	}

	po := bo.New()
	po.OriginURL = "http://10.0.0.2:9090"
	po.HealthCheck = ho.New()
	po.HealthCheck.Interval = timeconv.Duration(time.Second)
	if !NewTarget(h, st, testBackend(t, "probed", po)).Probed() {
		t.Error("a member with a health check interval reports as unprobed")
	}
}
