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

package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type stubHealthChecker struct {
	statuses healthcheck.StatusLookup
	subCh    chan bool
}

func (s *stubHealthChecker) Register(string, string, *ho.Options, *http.Client) (*healthcheck.Status, error) {
	return &healthcheck.Status{}, nil
}
func (s *stubHealthChecker) RegisterVirtual(string, string) *healthcheck.Status {
	return &healthcheck.Status{}
}
func (s *stubHealthChecker) Unregister(string)                  {}
func (s *stubHealthChecker) Status(string) *healthcheck.Status  { return nil }
func (s *stubHealthChecker) Statuses() healthcheck.StatusLookup { return s.statuses }
func (s *stubHealthChecker) Shutdown() {
	if s.subCh != nil {
		s.subCh <- true
	}
}
func (s *stubHealthChecker) Subscribe(ch chan bool) { s.subCh = ch }

type configBackend struct {
	mockBackend
	cfg *bo.Options
}

func (b *configBackend) Configuration() *bo.Options { return b.cfg }

func fixedNow() func() time.Time {
	tm := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return tm }
}

func TestStatusHandlerNilHealthChecker(t *testing.T) {
	if got := StatusHandler(time.Now, nil, nil); got != nil {
		t.Fatal("expected nil handler for nil health checker")
	}
}

func TestHealthStatusFormats(t *testing.T) {
	t.Parallel()

	bs := backendStatus{
		Name:                    "alb1",
		Provider:                providers.ALB,
		Mechanism:               names.MechanismRR,
		AvailablePoolMembers:    []string{"a"},
		UnavailablePoolMembers:  []string{"b"},
		UncheckedPoolMembers:    []string{"c"},
		InitializingPoolMembers: []string{"d"},
	}
	hs := &healthStatus{
		Title:      title,
		UpdateTime: "2024-06-01 12:00:00UTC",
		Unavailable: []backendStatus{{
			Name:      "down",
			Provider:  providers.Prometheus,
			DownSince: "2024-06-01 11:00:00UTC",
			Detail:    "connection refused",
		}},
		Available:    []backendStatus{bs, {Name: "up", Provider: providers.Prometheus}},
		Initializing: []backendStatus{{Name: "init", Provider: providers.Prometheus}},
		Unchecked:    []backendStatus{{Name: "nc", Provider: providers.Prometheus}},
	}

	if hs.String() == "" {
		t.Fatal("String() should delegate to Tabular")
	}
	if !strings.Contains(hs.JSON(), `"title":"Trickster Backend Health Status"`) {
		t.Fatalf("JSON() unexpected: %s", hs.JSON())
	}
	if !strings.Contains(hs.YAML(), "title:") {
		t.Fatalf("YAML() unexpected: %s", hs.YAML())
	}

	tab := hs.Tabular()
	for _, want := range []string{
		"down", "available", "unavailable since",
		"initializing", "not configured for automated health checks",
		"alb (rr)", "a:[a]", "u:[b]", "nc:[c]",
	} {
		if !strings.Contains(tab, want) {
			t.Errorf("Tabular() missing %q in:\n%s", want, tab)
		}
	}
}

func TestUpdateStatusTextBackendsAndALB(t *testing.T) {
	t.Parallel()

	now := fixedNow()
	passing := healthcheck.NewStatus("member-up", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil)
	failing := healthcheck.NewStatus("member-down", providers.Prometheus, "", healthcheck.StatusFailing, now().Add(-time.Hour), nil)
	failing.SetDetail("timeout")
	unchecked := healthcheck.NewStatus("member-nc", providers.Prometheus, "", healthcheck.StatusUnchecked, time.Time{}, nil)
	init := healthcheck.NewStatus("member-init", providers.Prometheus, "", healthcheck.StatusInitializing, time.Time{}, nil)

	rpOpts := bo.New()
	rpOpts.Provider = providers.ReverseProxyShort
	rpOpts.OriginURL = "http://example.com"
	rpOpts.HealthCheck = &ho.Options{Interval: timeconv.Duration(time.Minute)}

	noProbeOpts := bo.New()
	noProbeOpts.Provider = providers.ReverseProxyShort
	noProbeOpts.OriginURL = "http://example.com"
	noProbeOpts.HealthCheck = &ho.Options{Interval: 0}

	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismRR
	albOpts.ALBOptions.Pool = ao.Members("member-up", "member-down", "member-nc", "member-init")

	albClient, err := alb.NewClient("edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{
		"rp-up":       passing,
		"rp-down":     failing,
		"member-up":   passing,
		"member-down": failing,
		"member-nc":   unchecked,
		"member-init": init,
	}}
	hd := &healthDetail{}
	bes := backends.Backends{
		"rp-up":    &configBackend{mockBackend: mockBackend{name: "rp-up"}, cfg: rpOpts},
		"rp-down":  &configBackend{mockBackend: mockBackend{name: "rp-down"}, cfg: rpOpts},
		"no-probe": &configBackend{mockBackend: mockBackend{name: "no-probe"}, cfg: noProbeOpts},
		"edge":     albClient,
		"virtual-alb": &configBackend{
			mockBackend: mockBackend{name: "virtual-alb"},
			cfg:         &bo.Options{Provider: providers.ALB},
		},
	}

	updateStatusText(now, hc, hd, bes)
	d := hd.detail.Load()
	if d == nil {
		t.Fatal("expected detail to be stored")
	}
	if !strings.Contains(d.text, "member-up") || !strings.Contains(d.text, "member-down") {
		t.Fatalf("ALB pool detail missing members: %s", d.text)
	}
	if !strings.Contains(d.text, "no-probe") {
		t.Fatalf("expected unchecked backend without probe interval: %s", d.text)
	}
	if strings.Contains(d.json, "virtual-alb") {
		t.Fatalf("virtual ALB should not appear as plain backend entry: %s", d.json)
	}
	if !strings.Contains(d.json, `"name":"edge"`) {
		t.Fatalf("expected ALB section for edge: %s", d.json)
	}
}

func TestUpdateStatusTextUserRouterPool(t *testing.T) {
	t.Parallel()

	now := fixedNow()
	member := healthcheck.NewStatus("tenant-a", providers.ReverseProxyShort, "", healthcheck.StatusPassing, time.Time{}, nil)

	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismUR
	albOpts.ALBOptions.UserRouter = &uropt.Options{
		DefaultBackend: "tenant-a",
		Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "tenant-a"},
		},
	}

	albClient, err := alb.NewClient("ur-edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{"tenant-a": member}}
	hd := &healthDetail{}
	updateStatusText(now, hc, hd, backends.Backends{"ur-edge": albClient})

	d := hd.detail.Load()
	if !strings.Contains(d.text, "ur-edge") || !strings.Contains(d.text, "tenant-a") {
		t.Fatalf("expected user-router pool member in output: %s", d.text)
	}
}

func TestStatusHandlerContentNegotiation(t *testing.T) {
	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{
		"backend": healthcheck.NewStatus("backend", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil),
	}}
	handler := StatusHandler(fixedNow(), hc, nil)
	if handler == nil {
		t.Fatal("expected handler")
	}

	t.Run("yaml query param", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?yaml", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if ct := w.Header().Get(headers.NameContentType); ct != headers.ValueTextYAML {
			t.Fatalf("content-type = %q, want yaml", ct)
		}
		if !strings.HasPrefix(w.Body.String(), "title:") {
			t.Fatalf("expected yaml body, got %q", w.Body.String())
		}
	})

	t.Run("not modified", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(headers.NameIfModifiedSince, fixedNow()().Format(time.RFC1123))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", w.Code)
		}
	})

	t.Run("plaintext default", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "Trickster Backend Health Status") {
			t.Fatalf("expected plaintext tabular body, got %q", w.Body.String())
		}
	})
}

func TestStatusHelpers(t *testing.T) {
	t.Parallel()

	if got := statusToString(healthcheck.StatusPassing, false); got != "available" {
		t.Fatalf("passing = %q", got)
	}
	if got := statusToString(healthcheck.StatusFailing, true); got != "unavailable since" {
		t.Fatalf("failing since = %q", got)
	}
	if got := statusToString(healthcheck.StatusFailing, false); got != "unavailable" {
		t.Fatalf("failing = %q", got)
	}
	if got := formatProvider(backendStatus{Provider: providers.ALB, Mechanism: names.MechanismTSM}); got != "alb (tsm)" {
		t.Fatalf("formatProvider = %q", got)
	}
	if got := formatDetail(backendStatus{Provider: providers.ALB}); got != "" {
		t.Fatalf("empty ALB detail = %q, want empty", got)
	}
	if got := cleanupDescription(providers.ReverseProxyCache); got != providers.ReverseProxyCacheShort {
		t.Fatalf("cleanupDescription = %q", got)
	}
}

func TestStatusHandlerDefaultNow(t *testing.T) {
	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{
		"backend": healthcheck.NewStatus("backend", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil),
	}}
	handler := StatusHandler(nil, hc, nil)
	if handler == nil {
		t.Fatal("expected handler with default now")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	hc.Shutdown()
}

func TestStatusHandlerPanicRecovery(t *testing.T) {
	t.Run("panic before ready", func(t *testing.T) {
		panickingNow := func() time.Time {
			panic("forced builder panic")
		}
		hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{}}
		handler := StatusHandler(panickingNow, hc, nil)
		if handler == nil {
			t.Fatal("expected handler after panic recovery")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("panic after ready", func(t *testing.T) {
		calls := 0
		now := func() time.Time {
			calls++
			if calls > 1 {
				panic("forced rebuild panic")
			}
			return fixedNow()()
		}
		st := healthcheck.NewStatus("backend", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil)
		hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{"backend": st}}
		handler := StatusHandler(now, hc, nil)
		if handler == nil {
			t.Fatal("expected handler")
		}
		st.Set(healthcheck.StatusFailing)
		time.Sleep(50 * time.Millisecond)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		hc.Shutdown()
	})
}

func TestStatusHandlerNotifierRebuild(t *testing.T) {
	st := healthcheck.NewStatus("backend", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil)
	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{"backend": st}}
	handler := StatusHandler(fixedNow(), hc, nil)
	if handler == nil {
		t.Fatal("expected handler")
	}

	// Trigger a rebuild via status subscriber notification.
	st.Set(healthcheck.StatusFailing)
	time.Sleep(50 * time.Millisecond)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	hc.Shutdown()
}

func TestUpdateStatusTextEdgeCases(t *testing.T) {
	t.Parallel()

	now := fixedNow()
	passing := healthcheck.NewStatus("ok", providers.Prometheus, "", healthcheck.StatusPassing, time.Time{}, nil)

	ruleOpts := bo.New()
	ruleOpts.Provider = providers.Rule

	// Pool with only failing members (no unchecked) so the ALB is unavailable.
	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismRR
	albOpts.ALBOptions.Pool = ao.Members("down-only", "down-only")

	downOnly := healthcheck.NewStatus("down-only", providers.Prometheus, "", healthcheck.StatusFailing, now().Add(-time.Minute), nil)

	albClient, err := alb.NewClient("down-edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Separate ALB whose pool references an unknown member (nil status → unchecked).
	albMissingOpts := bo.New()
	albMissingOpts.Provider = providers.ALB
	albMissingOpts.ALBOptions = ao.New()
	albMissingOpts.ALBOptions.MechanismName = names.MechanismRR
	albMissingOpts.ALBOptions.Pool = ao.Members("missing-member")
	albMissingClient, err := alb.NewClient("missing-edge", albMissingOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewClient missing-edge: %v", err)
	}

	nilCfgALB := bo.New()
	nilCfgALB.Provider = providers.ALB
	nilCfgALB.ALBOptions = nil
	nilCfgClient, err := alb.NewClient("nil-cfg", nilCfgALB, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewClient nil-cfg: %v", err)
	}

	hc := &stubHealthChecker{statuses: healthcheck.StatusLookup{
		"ok":        passing,
		"down-only": downOnly,
		"rule-virt": healthcheck.NewStatus("rule-virt", providers.Rule, "", healthcheck.StatusPassing, time.Time{}, nil),
	}}
	hd := &healthDetail{}
	bes := backends.Backends{
		"ok":           &configBackend{mockBackend: mockBackend{name: "ok"}, cfg: &bo.Options{Provider: providers.Prometheus}},
		"nil-be":       nil,
		"rule-virt":    &configBackend{mockBackend: mockBackend{name: "rule-virt"}, cfg: ruleOpts},
		"down-edge":    albClient,
		"missing-edge": albMissingClient,
		"nil-cfg":      nilCfgClient,
		"no-cfg":       &configBackend{mockBackend: mockBackend{name: "no-cfg"}, cfg: nil},
	}

	updateStatusText(now, hc, hd, bes)
	d := hd.detail.Load()
	if d == nil {
		t.Fatal("expected detail")
	}
	if !strings.Contains(d.text, "down-edge") {
		t.Fatalf("expected unavailable ALB in output: %s", d.text)
	}
	if !strings.Contains(d.text, "missing-edge") {
		t.Fatalf("expected ALB with unchecked member in output: %s", d.text)
	}
	if strings.Contains(d.json, "rule-virt") {
		t.Fatalf("rule backend should be skipped as plain entry: %s", d.json)
	}
}
