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

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	dp "github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"

	"github.com/stretchr/testify/require"
)

func newTestTemplate(name string) *Options {
	o := New()
	o.Name = name
	o.Provider = providers.Prometheus
	o.IsTemplate = true
	return o
}

func TestValidateTemplateBackend(t *testing.T) {
	// a template does not require an origin_url
	o := newTestTemplate("t1")
	_, err := o.Validate()
	require.NoError(t, err)

	// a non-template origin backend still does
	o = New()
	o.Name = "b1"
	o.Provider = providers.Prometheus
	_, err = o.Validate()
	require.Error(t, err)

	// a template cannot be the default backend
	o = newTestTemplate("t1")
	o.IsDefault = true
	_, err = o.Validate()
	var tde *ErrTemplateIsDefault
	require.ErrorAs(t, err, &tde)

	// a template cannot use a non-origin provider
	o = newTestTemplate("t1")
	o.Provider = providers.ALB
	_, err = o.Validate()
	var tpe *ErrInvalidTemplateProvider
	require.ErrorAs(t, err, &tpe)
}

func newTestDiscoveryALB(name string) *Options {
	o := New()
	o.Name = name
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{
		MechanismName: "rr",
		Discovery: &ao.DiscoveryOptions{
			DiscovererName:  "in-cluster",
			TemplateBackend: "t1",
			Query:           &do.Query{Service: "prom"},
		},
	}
	return o
}

func testDiscoverers() do.Lookup {
	return do.Lookup{
		"in-cluster": &do.Options{Name: "in-cluster", Provider: dp.Kubernetes},
	}
}

func TestValidateDiscovery(t *testing.T) {
	l := Lookup{"t1": newTestTemplate("t1"), "alb1": newTestDiscoveryALB("alb1")}
	require.NoError(t, l.ValidateDiscovery(testDiscoverers()))

	// no discovery blocks at all is valid
	require.NoError(t, Lookup{"t1": newTestTemplate("t1")}.ValidateDiscovery(nil))

	// unknown discoverer name
	require.Error(t, l.ValidateDiscovery(do.Lookup{}))

	// query invalid for the discoverer's provider
	l["alb1"].ALBOptions.Discovery.Query = &do.Query{SRVName: "x"}
	require.Error(t, l.ValidateDiscovery(testDiscoverers()))
	l["alb1"].ALBOptions.Discovery.Query = &do.Query{Service: "prom"}

	// template_backend must exist
	l["alb1"].ALBOptions.Discovery.TemplateBackend = "missing"
	err := l.ValidateDiscovery(testDiscoverers())
	var tnErr *ErrInvalidTemplateBackendName
	require.ErrorAs(t, err, &tnErr)

	// ... and must actually be a template
	live := New()
	live.Name = "live"
	live.Provider = providers.Prometheus
	live.OriginURL = "http://example.com"
	l["live"] = live
	l["alb1"].ALBOptions.Discovery.TemplateBackend = "live"
	err = l.ValidateDiscovery(testDiscoverers())
	require.ErrorAs(t, err, &tnErr)

	// incomplete discovery block
	l["alb1"].ALBOptions.Discovery.TemplateBackend = ""
	require.Error(t, l.ValidateDiscovery(testDiscoverers()))
}

func TestValidateTemplatePoolMember(t *testing.T) {
	tmpl := newTestTemplate("t1")
	tmpl.TracingConfigName = ""
	tmpl.NegativeCacheName = ""
	alb := New()
	alb.Name = "alb1"
	alb.Provider = providers.ALB
	alb.TracingConfigName = ""
	alb.NegativeCacheName = ""
	alb.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.Members("t1")}
	l := Lookup{"t1": tmpl, "alb1": alb}
	err := l.ValidateConfigMappings(
		co.Lookup{"default": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	var tpm *ErrTemplatePoolMember
	require.ErrorAs(t, err, &tpm)
}

func TestValidateDiscoveryTSMTemplateRules(t *testing.T) {
	// an rp template is fine for non-TSM mechanisms without per-member groups
	l := Lookup{"t1": newTestTemplate("t1"), "alb1": newTestDiscoveryALB("alb1")}
	l["t1"].Provider = providers.ReverseProxyShort
	require.NoError(t, l.ValidateDiscovery(testDiscoverers()))

	// a tsmerge ALB requires a TSM-capable template provider
	l["alb1"].ALBOptions.MechanismName = "tsm"
	err := l.ValidateDiscovery(testDiscoverers())
	var tsmErr *ErrInvalidTemplateTSMProvider
	require.ErrorAs(t, err, &tsmErr)
	l["t1"].Provider = providers.Prometheus
	require.NoError(t, l.ValidateDiscovery(testDiscoverers()))

	// replica_group_label likewise requires a TSM-capable template
	l["alb1"].ALBOptions.MechanismName = "rr"
	l["t1"].Provider = providers.ReverseProxyShort
	l["alb1"].ALBOptions.Discovery.Query.ReplicaGroupLabel = "prometheus/replica"
	err = l.ValidateDiscovery(testDiscoverers())
	require.ErrorAs(t, err, &tsmErr)
	l["t1"].Provider = providers.Prometheus
	require.NoError(t, l.ValidateDiscovery(testDiscoverers()))
}
