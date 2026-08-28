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

package alb

import (
	goerrors "errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	pkgerrors "github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const invalidPoolMemberCheck = "invalid pool member name [invalid] provided for alb [test]"

type nestedTSMProviderStub struct {
	backends.Backend
	planned   bool
	finalized bool
}

func (s *nestedTSMProviderStub) PlanTSMMerge(r *http.Request,
	query string,
) (*tsmerge.TSMMergePlan, error) {
	s.planned = true
	return &tsmerge.TSMMergePlan{
		OriginalQuery: query,
		Variants: []tsmerge.TSMQueryVariant{{
			Name:              tsmerge.TSMVariantPrimary,
			Request:           r,
			MergeStrategy:     int(tsmerge.StrategySum),
			ResponseAuthority: true,
		}},
		Reduction: tsmerge.TSMReductionSpec{
			Kind:          tsmerge.TSMReductionStandard,
			InputVariants: tsmerge.TSMReductionPrimaryVariant(),
		},
		Completeness: tsmerge.TSMCompletenessResponseAuthority,
	}, nil
}

func (s *nestedTSMProviderStub) FinalizeTSMMerge(string, timeseries.Timeseries) {
	s.finalized = true
}

func TestHandlers(t *testing.T) {
	a := &ao.Options{
		MechanismName: names.MechanismFR,
		OutputFormat:  providers.Prometheus,
	}
	o := bo.New()
	o.ALBOptions = a

	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if _, ok := cl.Handlers()[providers.ALB]; !ok {
		t.Error("expected alb handler")
	}

	if _, ok := cl.Handlers()["localresponse"]; !ok {
		t.Error("expected localresponse handler")
	}

	a.MechanismName = names.MechanismFGR
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = names.MechanismNLM
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = string(tsm.ShortName)
	_, err = NewClient("test", o, nil, nil, nil, types.Lookup{providers.Prometheus: prometheus.NewClient})
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = names.MechanismRR
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	m := (&Client{}).DefaultPathConfigs(nil)
	if len(m) != 1 {
		t.Error("expected 1 got", len(m))
	}
}

func TestStartALBPools(t *testing.T) {
	err := StartALBPools(nil, nil)
	if err != nil {
		t.Error(err)
	}
	o := bo.New()
	cl, _ := NewClient("test", o, nil, nil, nil, nil)
	b := backends.Backends{"test": cl}
	err = StartALBPools(b, nil)
	if err == nil || err.Error() != "invalid options" {
		t.Error("expected err for invalid options, got", err)
	}
}

func TestValidateClients(t *testing.T) {
	err := ValidateClients(nil)
	if err != nil {
		t.Error(err)
	}
	o := bo.New()
	a := ao.New()
	a.MechanismName = "rx"
	a.Pool = ao.Members("invalid")

	o.ALBOptions = a
	o.Provider = providers.ALB
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != errors.ErrUnsupportedMechanism {
		t.Error("expected error for unsupported mechanism")
		return
	}
	a.MechanismName = names.MechanismRR
	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
		return
	}

	b := backends.Backends{"test": cl}
	err = ValidateClients(b)
	if err == nil || err.Error() != invalidPoolMemberCheck {
		t.Errorf("expected %s got %s", invalidPoolMemberCheck, err)
	}

	a.Pool = ao.Members("test")
	err = ValidateClients(b)
	if err != nil {
		t.Error(err)
	}

	o.Provider = "invalid"
	err = ValidateClients(b)
	if err != nil {
		t.Error(err)
	}
}

func TestValidateAndStartPool(t *testing.T) {
	o := bo.New()
	o.ALBOptions = nil
	tscl, _ := NewClient("test", o, nil, nil, nil, nil)
	cl := tscl.(*Client)

	err := cl.ValidateAndStartPool(nil, nil)
	if err == nil || err.Error() != "invalid options" {
		t.Error("expected error for invalid options, got ", err)
	}

	a := ao.New()
	o.ALBOptions = a
	b := backends.Backends{"test": cl}

	a.MechanismName = names.MechanismRR
	a.Pool = ao.Members("invalid")
	err = cl.ValidateAndStartPool(b, nil)
	if err == nil || err.Error() != invalidPoolMemberCheck {
		t.Error("expected error for invalid pool member name, got", err)
	}

	hcs := healthcheck.StatusLookup{
		"test": &healthcheck.Status{},
	}

	a.Pool = ao.Members("test")
	err = cl.ValidateAndStartPool(b, hcs)
	if err != nil {
		t.Error(err)
	}
}

func TestValidateAndStartTSMPoolRejectsIncompatibleProvider(t *testing.T) {
	memberOptions := bo.New()
	memberOptions.Provider = providers.ReverseProxyShort
	memberOptions.OriginURL = "http://example.com"
	member, err := backends.New("member", memberOptions, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	albOptions := bo.New()
	albOptions.Provider = providers.ALB
	albOptions.ALBOptions = ao.New()
	albOptions.ALBOptions.MechanismName = names.MechanismTSM
	albOptions.ALBOptions.Pool = ao.Members("member")
	base, err := backends.New("edge", albOptions, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{Backend: base}

	err = client.ValidateAndStartPool(backends.Backends{
		"edge": client, "member": member,
	}, healthcheck.StatusLookup{"member": &healthcheck.Status{}})
	if !goerrors.Is(err, errors.ErrInvalidTimeSeriesMergeProvider) {
		t.Fatalf("error = %v, want ErrInvalidTimeSeriesMergeProvider", err)
	}
}

func TestValidateTSMPoolMemberProviderResolvesNestedALB(t *testing.T) {
	leafOptions := bo.New()
	leafOptions.Provider = providers.Prometheus
	leafOptions.OriginURL = "http://example.com"
	leaf, err := backends.New("leaf", leafOptions, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	innerOptions := bo.New()
	innerOptions.Name = "inner"
	innerOptions.Provider = providers.ALB
	innerOptions.ALBOptions = ao.New()
	innerOptions.ALBOptions.MechanismName = names.MechanismRR
	innerOptions.ALBOptions.Pool = ao.Members("leaf")
	inner, err := backends.New("inner", innerOptions, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	clients := backends.Backends{"inner": inner, "leaf": leaf}
	if err := validateTSMPoolMemberProvider("inner", clients, sets.NewStringSet()); err != nil {
		t.Fatalf("nested Prometheus ALB rejected: %v", err)
	}
}

func TestNestedALBDelegatesTSMProvider(t *testing.T) {
	leafOptions := bo.New()
	leafOptions.Provider = providers.Prometheus
	leafOptions.Prometheus = &prop.Options{Labels: map[string]string{
		"route": "a",
	}}
	leafBackend, err := backends.New("leaf", leafOptions, nil,
		http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &nestedTSMProviderStub{Backend: leafBackend}

	innerOptions := bo.New()
	innerOptions.Provider = providers.ALB
	innerOptions.ALBOptions = ao.New()
	innerOptions.ALBOptions.MechanismName = names.MechanismRR
	innerOptions.ALBOptions.Pool = ao.Members("leaf")
	innerBackend, err := NewClient("inner", innerOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner := innerBackend.(*Client)
	if err := inner.ValidateAndStartPool(backends.Backends{
		"inner": inner,
		"leaf":  leaf,
	}, nil); err != nil {
		t.Fatal(err)
	}
	defer inner.StopPool()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/query?query=sum(up)", nil)
	plan, err := inner.PlanTSMMerge(r, "sum(up)")
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.planned || plan.Variants[0].MergeStrategy != int(tsmerge.StrategySum) {
		t.Fatalf("nested plan = %+v, terminal provider was not used", plan)
	}
	inner.FinalizeTSMMerge("sum(up)", nil)
	if !leaf.finalized {
		t.Fatal("nested finalizer did not reach terminal provider")
	}
	if got := inner.TSMInjectedLabelKeys(); !slices.Equal(got, []string{"route"}) {
		t.Fatalf("nested injected label keys = %v", got)
	}
}

func TestValidateClientsAllowsReplicaGroupOnNestedTSMMember(t *testing.T) {
	leafOptions := bo.New()
	leafOptions.Provider = providers.Prometheus
	leaf, err := backends.New("leaf", leafOptions, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	innerOptions := bo.New()
	innerOptions.Name = "inner"
	innerOptions.Provider = providers.ALB
	innerOptions.ReplicaGroup = "shard-a"
	innerOptions.ALBOptions = ao.New()
	innerOptions.ALBOptions.MechanismName = names.MechanismRR
	innerOptions.ALBOptions.Pool = ao.Members("leaf")
	innerBackend, err := NewClient("inner", innerOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	outerOptions := bo.New()
	outerOptions.Name = "outer"
	outerOptions.Provider = providers.ALB
	outerOptions.ALBOptions = ao.New()
	outerOptions.ALBOptions.MechanismName = names.MechanismTSM
	outerOptions.ALBOptions.OutputFormat = providers.Prometheus
	outerOptions.ALBOptions.Pool = ao.Members("inner")
	outerBackend, err := NewClient("outer", outerOptions, nil, nil, nil,
		types.Lookup{providers.Prometheus: prometheus.NewClient})
	if err != nil {
		t.Fatal(err)
	}

	clients := backends.Backends{
		"leaf":  leaf,
		"inner": innerBackend,
		"outer": outerBackend,
	}
	if err := ValidateClients(clients); err != nil {
		t.Fatalf("nested TSM replica group rejected: %v", err)
	}

	outerOptions.ALBOptions.Pool = ao.Members("leaf")
	if err := ValidateClients(clients); err == nil {
		t.Fatal("expected custom replica group on non-TSM-member ALB to be rejected")
	}
}

func TestNewClientCaptureDefaults(t *testing.T) {
	o := bo.New()
	o.MaxCaptureBytes = 123
	o.MaxFanoutCaptureBytes = 456
	o.ALBOptions = ao.New()
	o.ALBOptions.MechanismName = names.MechanismRR

	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.ALBOptions.MaxCaptureBytes != 123 || o.ALBOptions.MaxFanoutCaptureBytes != 456 {
		t.Fatalf("expected ALB capture defaults to inherit backend values, got %d/%d",
			o.ALBOptions.MaxCaptureBytes, o.ALBOptions.MaxFanoutCaptureBytes)
	}
	if cl == nil {
		t.Fatal("expected client")
	}
}

func TestStopPoolsAndStopPool(t *testing.T) {
	o := bo.New()
	o.ALBOptions = ao.New()
	o.ALBOptions.MechanismName = names.MechanismRR
	o.ALBOptions.Pool = ao.Members("test")

	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := backends.Backends{"test": cl}
	if err := StopPools(b); err != nil {
		t.Fatalf("StopPools: %v", err)
	}
	cl.(*Client).StopPool()
}

func TestClientValidateErrors(t *testing.T) {
	o := bo.New()
	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := cl.(*Client)

	if err := c.Validate(sets.NewStringSet()); err != pkgerrors.ErrInvalidOptions {
		t.Fatalf("Validate() = %v, want ErrInvalidOptions", err)
	}

	o.ALBOptions = ao.New()
	o.ALBOptions.MechanismName = "missing"
	if err := c.Validate(sets.NewStringSet()); err == nil {
		t.Fatal("expected invalid mechanism error")
	}
}

func TestValidateAndStartPoolUnprobedMembersResetFloor(t *testing.T) {
	memberOpts := bo.New()
	memberOpts.Provider = providers.ReverseProxyShort
	memberOpts.OriginURL = "http://example.com"
	memberOpts.HealthCheck.Interval = 0

	member, err := backends.New("member", memberOpts, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	albOpts := bo.New()
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismRR
	albOpts.ALBOptions.HealthyFloor = int(healthcheck.StatusPassing)
	albOpts.ALBOptions.Pool = ao.Members("member")

	albClient, err := NewClient("edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := albClient.(*Client)

	err = cl.ValidateAndStartPool(backends.Backends{
		"edge":   cl,
		"member": member,
	}, healthcheck.StatusLookup{"member": &healthcheck.Status{}})
	if err != nil {
		t.Fatalf("ValidateAndStartPool: %v", err)
	}
	cl.StopPool()
}

func TestValidateAndStartPoolAdmitsFailingFloor(t *testing.T) {
	memberOpts := bo.New()
	memberOpts.Provider = providers.ReverseProxyShort
	memberOpts.OriginURL = "http://example.com"
	member, err := backends.New("member", memberOpts, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}

	albOpts := bo.New()
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismRR
	albOpts.ALBOptions.HealthyFloor = int(healthcheck.StatusFailing)
	albOpts.ALBOptions.Pool = ao.Members("member")

	albClient, err := NewClient("edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := albClient.(*Client)

	err = cl.ValidateAndStartPool(backends.Backends{
		"edge":   cl,
		"member": member,
	}, healthcheck.StatusLookup{"member": &healthcheck.Status{}})
	if err != nil {
		t.Fatalf("ValidateAndStartPool: %v", err)
	}
	cl.StopPool()
}

func TestValidateAndStartUserRouter(t *testing.T) {
	defaultHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	memberOpts := bo.New()
	memberOpts.Provider = providers.ReverseProxyShort
	memberOpts.OriginURL = "http://example.com"
	member, err := backends.New("tenant-a", memberOpts, nil, defaultHandler, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("default backend", func(t *testing.T) {
		albOpts := bo.New()
		albOpts.ALBOptions = ao.New()
		albOpts.ALBOptions.MechanismName = names.MechanismUR
		albOpts.ALBOptions.UserRouter = &uropt.Options{
			DefaultBackend: "tenant-a",
			TargetProvider: providers.ReverseProxyShort,
			Users: uropt.UserMappingOptionsByUser{
				"alice": {ToBackend: "tenant-a", ToUser: "alice"},
			},
		}

		albClient, err := NewClient("ur-edge", albOpts, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cl := albClient.(*Client)
		h, ok := cl.handler.(*ur.Handler)
		if !ok {
			t.Fatalf("handler type = %T, want *ur.Handler", cl.handler)
		}

		err = cl.validateAndStartUserRouter(backends.Backends{
			"tenant-a": member,
		}, healthcheck.StatusLookup{"tenant-a": &healthcheck.Status{}})
		if err != nil {
			t.Fatalf("validateAndStartUserRouter: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("unauthenticated UR request status = %d, want 200 via default backend", w.Code)
		}
	})

	t.Run("no route unauthorized", func(t *testing.T) {
		albOpts := bo.New()
		albOpts.ALBOptions = ao.New()
		albOpts.ALBOptions.MechanismName = names.MechanismUR
		albOpts.ALBOptions.UserRouter = &uropt.Options{
			TargetProvider:    providers.ReverseProxyShort,
			NoRouteStatusCode: http.StatusUnauthorized,
		}

		albClient, err := NewClient("ur-edge", albOpts, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cl := albClient.(*Client)
		h := cl.handler.(*ur.Handler)

		if err := cl.validateAndStartUserRouter(backends.Backends{}, nil); err != nil {
			t.Fatalf("validateAndStartUserRouter: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated UR request status = %d, want 401", w.Code)
		}
	})

	t.Run("effective no route status does not mutate options", func(t *testing.T) {
		albOpts := bo.New()
		albOpts.ALBOptions = ao.New()
		albOpts.ALBOptions.MechanismName = names.MechanismUR
		albOpts.ALBOptions.UserRouter = &uropt.Options{
			TargetProvider:    providers.ReverseProxyShort,
			NoRouteStatusCode: http.StatusOK,
		}

		albClient, err := NewClient("ur-edge", albOpts, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cl := albClient.(*Client)
		h := cl.handler.(*ur.Handler)

		if err := cl.validateAndStartUserRouter(backends.Backends{}, nil); err != nil {
			t.Fatalf("validateAndStartUserRouter: %v", err)
		}
		if got := albOpts.ALBOptions.UserRouter.NoRouteStatusCode; got != http.StatusOK {
			t.Fatalf("NoRouteStatusCode mutated to %d, want original %d",
				got, http.StatusOK)
		}

		req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("unauthenticated UR request status = %d, want effective 502", w.Code)
		}
	})
}

func TestValidateAndStartUserRouterErrors(t *testing.T) {
	albOpts := bo.New()
	albOpts.ALBOptions = ao.New()
	albOpts.ALBOptions.MechanismName = names.MechanismUR
	albOpts.ALBOptions.UserRouter = &uropt.Options{
		DefaultBackend: "missing",
		TargetProvider: providers.ReverseProxyShort,
	}

	albClient, err := NewClient("ur-edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := albClient.(*Client)

	err = cl.validateAndStartUserRouter(backends.Backends{}, nil)
	if err == nil {
		t.Fatal("expected invalid default backend error")
	}

	albOpts.ALBOptions.UserRouter = &uropt.Options{
		TargetProvider: providers.ReverseProxyShort,
		Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "missing"},
		},
	}
	albClient, err = NewClient("ur-edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl = albClient.(*Client)
	err = cl.validateAndStartUserRouter(backends.Backends{}, nil)
	if err == nil {
		t.Fatal("expected invalid user backend error")
	}

	albOpts.ALBOptions.UserRouter = &uropt.Options{
		TargetProvider: providers.ReverseProxyShort,
		Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "tenant-a", ToCredential: "secret"},
		},
	}
	albClient, err = NewClient("ur-edge", albOpts, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl = albClient.(*Client)
	memberOpts := bo.New()
	memberOpts.Provider = providers.ReverseProxyShort
	memberOpts.OriginURL = "http://example.com"
	member, err := backends.New("tenant-a", memberOpts, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = cl.validateAndStartUserRouter(backends.Backends{"tenant-a": member}, nil)
	if _, ok := goerrors.AsType[*errors.InvalidALBOptionsError](err); !ok {
		t.Fatalf("validateAndStartUserRouter() = %v, want InvalidALBOptionsError", err)
	}
	want := errors.NewErrInvalidUserRouterCreds("ur-edge")
	if err.Error() != want.Error() {
		t.Fatalf("validateAndStartUserRouter() = %v, want %v", err, want)
	}
}

func TestObserveOnlyOpts(t *testing.T) {
	opts := observeOnlyOpts()
	if opts == nil || !opts.ObserveOnly {
		t.Fatalf("observeOnlyOpts() = %+v", opts)
	}
}
