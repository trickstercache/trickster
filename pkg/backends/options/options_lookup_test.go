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

package options

import (
	"testing"
	"time"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

func TestLookupValidateAndInitialize(t *testing.T) {
	t.Parallel()

	member := New()
	member.Name = "member"
	member.Provider = providers.ReverseProxyShort
	member.OriginURL = "http://example.com"
	member.TracingConfigName = ""

	alb := New()
	alb.Name = "edge"
	alb.Provider = providers.ALB
	alb.ALBOptions = ao.New()
	alb.ALBOptions.MechanismName = "rr"
	alb.ALBOptions.UserRouter = &uropt.Options{
		DefaultBackend: "member",
	}

	l := Lookup{"member": member, "edge": alb}
	if err := l.Validate(); err != nil {
		t.Fatalf("Lookup.Validate: %v", err)
	}
	if alb.ALBOptions.UserRouter.TargetProvider != providers.ReverseProxyShort {
		t.Fatalf("UserRouter.TargetProvider = %q, want %q",
			alb.ALBOptions.UserRouter.TargetProvider, providers.ReverseProxyShort)
	}

	if err := l.Initialize(); err != nil {
		t.Fatalf("Lookup.Initialize: %v", err)
	}
	if member.Name != "member" {
		t.Fatalf("member.Name = %q", member.Name)
	}
}

func TestLookupValidateRejectsInvalidOriginURL(t *testing.T) {
	t.Parallel()

	o := New()
	o.Provider = providers.Prometheus
	o.OriginURL = "://bad-url"
	l := Lookup{"bad": o}
	_, err := o.Validate()
	if err == nil {
		t.Fatal("expected invalid origin_url error")
	}
	if err := l.Validate(); err == nil {
		t.Fatal("expected Lookup.Validate to propagate origin_url error")
	}
}

func TestValidateConfigMappingsSuccessPaths(t *testing.T) {
	t.Parallel()

	o := New()
	o.Name = "backend"
	o.Provider = providers.Prometheus
	o.OriginURL = "http://example.com"
	o.AuthenticatorName = "auth"
	o.ReqRewriterName = "rw"
	o.TracingConfigName = "trace"
	o.NegativeCacheName = "neg"
	o.Paths = po.List{{
		Path:              "/secure",
		AuthenticatorName: "auth",
		ReqRewriterName:   "rw",
	}}

	l := Lookup{"backend": o}
	err := l.ValidateConfigMappings(
		co.Lookup{"default": nil},
		negative.Lookups{"neg": {404: time.Second}},
		ro.Lookup{},
		rwopts.Lookup{"rw": nil},
		autho.Lookup{"auth": autho.New()},
		tro.Lookup{"trace": tro.New()},
	)
	if err != nil {
		t.Fatalf("ValidateConfigMappings: %v", err)
	}
	if o.AuthOptions == nil {
		t.Fatal("expected AuthOptions to be wired")
	}
	if len(o.NegativeCache) == 0 {
		t.Fatal("expected NegativeCache map to be populated")
	}
}

func TestValidateConfigMappingsALBAndCycles(t *testing.T) {
	t.Parallel()

	member := New()
	member.Name = "member"
	member.Provider = providers.ReverseProxyShort
	member.OriginURL = "http://example.com"
	member.TracingConfigName = ""
	member.NegativeCacheName = ""

	edge := New()
	edge.Name = "edge"
	edge.Provider = providers.ALB
	edge.TracingConfigName = ""
	edge.NegativeCacheName = ""
	edge.ALBOptions = ao.New()
	edge.ALBOptions.MechanismName = "rr"
	edge.ALBOptions.Pool = []string{"member"}

	l := Lookup{"member": member, "edge": edge}
	err := l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err != nil {
		t.Fatalf("ValidateConfigMappings for ALB pool: %v", err)
	}

	edge.ALBOptions.Pool = []string{"edge"}
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Fatal("expected cycle validation error")
	}
}

func TestValidateConfigMappingsInvalidReferences(t *testing.T) {
	t.Parallel()

	o := New()
	o.Name = "backend"
	o.Provider = providers.Prometheus
	o.OriginURL = "http://example.com"
	o.AuthenticatorName = "missing"
	l := Lookup{"backend": o}

	err := l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Fatal("expected invalid authenticator error")
	}

	o.AuthenticatorName = ""
	o.Paths = po.List{{Path: "/x", AuthenticatorName: "missing"}}
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Fatal("expected invalid path authenticator error")
	}

	o.Paths = nil
	o.Provider = providers.ALB
	o.ALBOptions = nil
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Fatal("expected invalid ALB options error")
	}
}

func TestOptionsValidateHealthCheckError(t *testing.T) {
	t.Parallel()

	o := New()
	o.Name = "backend"
	o.Provider = providers.Prometheus
	o.OriginURL = "http://example.com"
	o.HealthCheck = &ho.Options{Verb: "NOT_A_METHOD"}

	_, err := o.Validate()
	if err == nil {
		t.Fatal("expected healthcheck validation error")
	}
}
