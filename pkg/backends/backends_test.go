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

package backends

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func TestBackends(t *testing.T) {
	cl, _ := New("test1", bo.New(), nil, lm.NewRouter(), nil)
	o := Backends{"test1": cl}

	c := o.Get("test1")
	if c == nil {
		t.Error("expected non-nil client")
	}

	c = o.Get("invalid")
	if c != nil {
		t.Error("expected nil client")
	}

	cfg := o.GetConfig("test1")
	if cfg == nil {
		t.Error("expected non-nil config")
	}

	cfg = o.GetConfig("invalid")
	if cfg != nil {
		t.Error("expected nil config")
	}

	r := o.GetRouter("test1")
	if r == nil {
		t.Error("expected non-nil router")
	}

	r = o.GetRouter("invalid")
	if r != nil {
		t.Error("expected nil router")
	}
}

func TestIsVirtual(t *testing.T) {
	if ok := IsVirtual(providers.Rule); !ok {
		t.Error("expected true")
	}

	if ok := IsVirtual(providers.Prometheus); ok {
		t.Error("expected false")
	}
}

func TestStartHealthChecks(t *testing.T) {
	// 1: rule / Virtual provider
	o1 := bo.New()
	o1.Provider = providers.Rule
	c1, _ := New("test1", o1, nil, lm.NewRouter(), nil)

	// 2: non-virtual provider with no health check options
	o2 := bo.New()
	c2, _ := New("test2", o2, nil, lm.NewRouter(), nil)

	b := Backends{"test1": c1}
	_, err := b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	o2.HealthCheck = nil
	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	o2.HealthCheck = ho.New()
	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	c2a := &testBackend{Backend: c2}
	b = Backends{"test1": c1, "test2": c2a}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	c2p := &protocolTestBackend{Backend: c2}
	b = Backends{"test2": c2p}
	hc, err := b.StartHealthChecks(nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	hc.Status("test2").Prober()(w)
	if c2p.calls != 1 || w.Code != 200 {
		t.Fatalf("protocol health probe calls = %d, status = %d", c2p.calls, w.Code)
	}
}

type testBackend struct {
	Backend
}

func (tb *testBackend) DefaultHealthCheckConfig() *ho.Options {
	return ho.New()
}

type protocolTestBackend struct {
	testBackend
	calls int
}

func (tb *protocolTestBackend) HealthCheckProbe() healthcheck.Probe {
	return func(context.Context) error {
		tb.calls++
		return nil
	}
}

func TestUsesCache(t *testing.T) {
	b := UsesCache(providers.ReverseProxyShort)
	if b {
		t.Error("expected false")
	}
	if UsesCache(providers.Static) {
		t.Error("expected false")
	}
	if !UsesCache(providers.Prometheus) {
		t.Error("expected true")
	}
}

func TestHasOrigin(t *testing.T) {
	for _, provider := range []string{providers.ALB, providers.Rule, providers.Static} {
		if HasOrigin(provider) {
			t.Errorf("expected %s to have no origin", provider)
		}
	}
	for _, provider := range []string{providers.Prometheus, providers.ReverseProxyCache} {
		if !HasOrigin(provider) {
			t.Errorf("expected %s to have an origin", provider)
		}
	}
	// static answers locally, but does not front other backends
	if IsVirtual(providers.Static) {
		t.Error("expected static not to be virtual")
	}
}

// choosyBackend probes by protocol only when it has a probe to offer, and may refuse to be
// probed at all
type choosyBackend struct {
	testBackend
	probe   healthcheck.Probe
	refusal string
}

func (tb *choosyBackend) HealthCheckProbe() healthcheck.Probe { return tb.probe }

func (tb *choosyBackend) HealthCheckUnsupported() string { return tb.refusal }

func TestStartHealthChecksByWhatABackendOffers(t *testing.T) {
	newBackend := func(name string) Backend {
		o := bo.New()
		o.HealthCheck = ho.New()
		o.HealthCheck.Interval = 0
		c, err := New(name, o, nil, lm.NewRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var probed int
	b := Backends{
		// a backend with a protocol probe is probed with it
		"protocol": &choosyBackend{Backend: newBackend("protocol"),
			probe: func(context.Context) error { probed++; return nil }},
		// one with none to offer for its origin falls back to the request probe
		"request": &choosyBackend{Backend: newBackend("request")},
		// one that cannot be probed is left out rather than probed in a way that must fail
		"refuses": &choosyBackend{Backend: newBackend("refuses"), refusal: "no probe for this origin"},
	}
	b["refuses"].Configuration().HealthCheck.Interval = 1
	hc, err := b.StartHealthChecks(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hc.Shutdown()
	statuses := hc.Statuses()
	if statuses["protocol"] == nil || statuses["request"] == nil {
		t.Fatalf("registered = %v", statuses)
	}
	if statuses["refuses"] != nil {
		t.Error("a backend that cannot be probed was registered for a probe")
	}
	w := httptest.NewRecorder()
	statuses["protocol"].Prober()(w)
	if probed != 1 {
		t.Errorf("the protocol probe ran %d times", probed)
	}
}

type statusOwner struct {
	Backend
	status *healthcheck.Status
}

func (o *statusOwner) HealthStatus() *healthcheck.Status { return o.status }

// requestOnlyChecker is a health checker that cannot register a protocol probe
type requestOnlyChecker struct{ healthcheck.HealthChecker }

func TestVirtualBackendsReportTheirOwnStatus(t *testing.T) {
	virtual := func(name string) Backend {
		o := bo.New()
		o.Provider = providers.ALB
		c, err := New(name, o, nil, lm.NewRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	own := healthcheck.NewStatus("follows", providers.ALB, "", healthcheck.StatusFailing, time.Time{}, nil)
	hc, err := Backends{
		"follows":   &statusOwner{Backend: virtual("follows"), status: own},
		"keeps":     &statusOwner{Backend: virtual("keeps")},
		"synthetic": virtual("synthetic"),
	}.StartHealthChecks(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hc.Shutdown()
	statuses := hc.Statuses()
	if statuses["follows"] != own {
		t.Error("a virtual backend's own status is not the one reported")
	}
	for _, name := range []string{"keeps", "synthetic"} {
		if st := statuses[name]; st == nil || st.Get() != healthcheck.StatusPassing {
			t.Errorf("%s: status = %v", name, st)
		}
	}
}

func TestRegisterHealthCheckNeedsAProbeRegistrar(t *testing.T) {
	o := bo.New()
	o.HealthCheck = ho.New()
	c, err := New("protocol", o, nil, lm.NewRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hc := healthcheck.New()
	defer hc.Shutdown()
	probed := &choosyBackend{Backend: c, probe: func(context.Context) error { return nil }}
	if _, err := RegisterHealthCheck(requestOnlyChecker{hc}, "protocol", "test", probed); !errors.Is(err, ErrNoProbeRegistrar) {
		t.Errorf("error = %v", err)
	}
	if _, err := (Backends{"protocol": probed}).StartHealthChecks(nil); err != nil {
		t.Errorf("a full health checker refused a protocol probe: %v", err)
	}
	bare, err := New("bare", bo.New(), nil, lm.NewRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bare.Configuration().HealthCheck = nil
	if st, err := RegisterHealthCheck(hc, "bare", "test", bare); st != nil || err != nil {
		t.Errorf("a backend with no health check: %v, %v", st, err)
	}
}
