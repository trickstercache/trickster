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
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	pkgerrors "github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const invalidPoolMemberCheck = "invalid pool member name [invalid] provided for alb [test]"

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
	a.Pool = []string{"invalid"}

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

	a.Pool = []string{"test"}
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
	a.Pool = []string{"invalid"}
	err = cl.ValidateAndStartPool(b, nil)
	if err == nil || err.Error() != invalidPoolMemberCheck {
		t.Error("expected error for invalid pool member name, got", err)
	}

	hcs := healthcheck.StatusLookup{
		"test": &healthcheck.Status{},
	}

	a.Pool = []string{"test"}
	err = cl.ValidateAndStartPool(b, hcs)
	if err != nil {
		t.Error(err)
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
	o.ALBOptions.Pool = []string{"test"}

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
	albOpts.ALBOptions.Pool = []string{"member"}

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
	albOpts.ALBOptions.Pool = []string{"member"}

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
			DefaultBackend:   "tenant-a",
			TargetProvider:   providers.ReverseProxyShort,
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
	var credErr *errors.InvalidALBOptionsError
	if !goerrors.As(err, &credErr) {
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
