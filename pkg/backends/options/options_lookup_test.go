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
	"errors"
	"testing"
	"time"

	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

func TestLookupValidateAndInitialize(t *testing.T) {
	t.Parallel()

	member := New()
	member.Name = keys.Member
	member.Provider = providers.ReverseProxyShort
	member.OriginURL = "http://example.com"
	member.TracingConfigName = ""

	alb := New()
	alb.Name = "edge"
	alb.Provider = providers.ALB
	alb.ALBOptions = ao.New()
	alb.ALBOptions.MechanismName = "rr"
	alb.ALBOptions.UserRouter = &uropt.Options{
		DefaultBackend: keys.Member,
	}

	l := Lookup{keys.Member: member, "edge": alb}
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
	if member.Name != keys.Member {
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
	}, {
		Path:              "/public",
		AuthenticatorName: reserved.ReferenceNone,
	}}

	l := Lookup{"backend": o}
	err := l.ValidateConfigMappings(
		co.Lookup{"default": nil},
		negative.Lookups{"neg": {404: time.Second}},
		ro.Lookup{},
		rwopts.Lookup{"rw": nil},
		autho.Lookup{"auth": autho.New()},
		tro.Lookup{"trace": tro.New()},
		nil,
	)
	if err != nil {
		t.Fatalf("ValidateConfigMappings: %v", err)
	}
	if o.AuthOptions == nil {
		t.Fatal("expected AuthOptions to be wired")
	}
	if o.Paths[0].AuthOptions == nil || o.Paths[1].AuthOptions != nil {
		t.Fatalf("path AuthOptions = %v, %v; want the named authenticator, then none for %q",
			o.Paths[0].AuthOptions, o.Paths[1].AuthOptions, reserved.ReferenceNone)
	}
	if len(o.NegativeCache) == 0 {
		t.Fatal("expected NegativeCache map to be populated")
	}
}

func TestValidateConfigMappingsALBAndCycles(t *testing.T) {
	t.Parallel()

	member := New()
	member.Name = keys.Member
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
	edge.ALBOptions.Pool = ao.Members(keys.Member)

	l := Lookup{keys.Member: member, "edge": edge}
	err := l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, nil)
	if err != nil {
		t.Fatalf("ValidateConfigMappings for ALB pool: %v", err)
	}

	edge.ALBOptions.Pool = ao.Members("edge")
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, nil)
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
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, nil)
	if err == nil {
		t.Fatal("expected invalid authenticator error")
	}

	o.AuthenticatorName = ""
	o.Paths = po.List{{Path: "/x", AuthenticatorName: "missing"}}
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, nil)
	if err == nil {
		t.Fatal("expected invalid path authenticator error")
	}

	o.Paths = nil
	o.Provider = providers.ALB
	o.ALBOptions = nil
	err = l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, nil)
	if err == nil {
		t.Fatal("expected invalid ALB options error")
	}
}

func TestIPACLMappings(t *testing.T) {
	t.Parallel()

	acls := ipacl.Lookup{
		"office": {Allow: []string{"10.0.0.0/8"}},
		"edge":   {Allow: []string{"10.20.0.0/24"}, Source: "peer"},
	}
	if _, err := acls.Validate(); err != nil {
		t.Fatal(err)
	}

	backend := func() *Options {
		o := New()
		o.Name = "backend"
		o.Provider = providers.Prometheus
		o.OriginURL = "http://example.com"
		o.TracingConfigName = ""
		o.NegativeCacheName = ""
		return o
	}
	mappings := func(l Lookup) error {
		return l.ValidateConfigMappings(co.Lookup{"default": nil}, negative.Lookups{},
			ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{}, acls)
	}

	o := backend()
	o.IPACLName = "office"
	o.Paths = po.List{
		{Path: "/admin/", IPACLName: "office"},
		{Path: "/public/", IPACLName: reserved.ReferenceNone},
		{Path: "/open/"},
	}
	if err := mappings(Lookup{"backend": o}); err != nil {
		t.Fatal(err)
	}
	if o.IPACL != acls["office"].Compiled || o.Paths[0].IPACL != acls["office"].Compiled {
		t.Fatal("named lists were not resolved")
	}
	if o.Paths[1].IPACL != nil || o.Paths[1].IPACLName != reserved.ReferenceNone || o.Paths[2].IPACL != nil {
		t.Fatalf("path lists = %#v, %#v", o.Paths[1], o.Paths[2])
	}
	cloned := o.Clone()
	if cloned.IPACL != o.IPACL || cloned.Paths[0].IPACL != o.Paths[0].IPACL {
		t.Fatal("clone copied the compiled list")
	}

	o = backend()
	o.IPACLName = "missing"
	err := mappings(Lookup{"backend": o})
	var missing *ErrInvalidIPACLName
	if !errors.As(err, &missing) {
		t.Fatalf("missing backend list = %v", err)
	}

	o.IPACLName = reserved.ReferenceNone
	if err = mappings(Lookup{"backend": o}); !errors.As(err, &missing) {
		t.Fatalf("backend none = %v", err)
	}

	o.IPACLName = "edge"
	var peer *ErrIPACLSourcePeer
	if err = mappings(Lookup{"backend": o}); !errors.As(err, &peer) {
		t.Fatalf("backend peer = %v", err)
	}

	o = backend()
	o.Paths = po.List{{Path: "/admin/", IPACLName: "missing"}}
	if err = mappings(Lookup{"backend": o}); !errors.As(err, &missing) {
		t.Fatalf("missing path list = %v", err)
	}
	o.Paths[0].IPACLName = "edge"
	if err = mappings(Lookup{"backend": o}); !errors.As(err, &peer) {
		t.Fatalf("path peer = %v", err)
	}

	tmpl := backend()
	tmpl.Name = "tmpl"
	tmpl.IsTemplate = true
	tmpl.IPACLName = "office"
	if err = mappings(Lookup{"tmpl": tmpl}); err != nil {
		t.Fatal(err)
	}
	if tmpl.Clone().IPACL != tmpl.IPACL || tmpl.IPACL != acls["office"].Compiled {
		t.Fatal("template did not keep the compiled list")
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
