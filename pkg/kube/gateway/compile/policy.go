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

package compile

import (
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func (i index) policy(rule ir.Rule) *ir.Policy {
	return i.named(rule.Policy)
}

func (i index) named(name string) *ir.Policy {
	if name == "" {
		return nil
	}
	p, ok := i.policies[name]
	if !ok {
		return nil
	}
	return &p
}

// effective is the settings a generated backend is built from: the
// kubernetes defaults with the rule's Policy overrides applied on top
type effective struct {
	routingMode string
	cacheName   string
	// negativeCacheName, tracingName, rewriterName and authenticatorName are operator-configured
	// objects a generated backend references; only the negative cache is a route's to override
	negativeCacheName string
	tracingName       string
	rewriterName      string
	authenticatorName string
	timeout           time.Duration
	handlerName       string
	accessLog         *alo.Options
	// healthMode and healthCheck shape the endpoint mode's ALBs: how a discovered member is
	// judged healthy, and the probe it runs when that is by probing
	healthMode  string
	healthCheck *ho.Options
	// tsProvider is the time series provider the generated backend is, or empty for a plain
	// reverse proxy; a provider accelerates its own API paths and always caches
	tsProvider string
	// policy is the rule's resolved policy, or nil when it names none; the
	// fields below it carry no default and are read straight from it
	policy *ir.Policy
	// grpc marks a backend built for a gRPC route: served by the proxy handler over HTTP/2,
	// relaying trailers, and caching nothing
	grpc bool
	// source is the object the backend is generated for, named in its access log lines
	source ir.Source
}

func (e effective) forRoute(r *ir.Route) effective {
	// the settings adjusted for the route the rule belongs to
	e.source = r.Source
	if r.Protocol == ir.ProtocolGRPC {
		e.grpc = true
		e.handlerName = handlerProxy
		e.tsProvider = ""
	}
	return e
}

func (e effective) accessLogFor() *alo.Options {
	// the source object is named in the extra values when the operator configured an access log
	if e.accessLog == nil || e.source.Kind == "" {
		return e.accessLog
	}
	out := e.accessLog.Clone()
	if out.Extra == nil {
		out.Extra = make(map[string]string, 3)
	}
	out.Extra[extraRouteKind] = e.source.Kind
	out.Extra[extraRouteNamespace] = e.source.Namespace
	out.Extra[extraRouteName] = e.source.Name
	return out
}

// access log extra keys naming the object a generated backend serves
const (
	extraRouteKind      = "route_kind"
	extraRouteNamespace = "route_namespace"
	extraRouteName      = "route_name"
)

// resolveMember resolves the settings one member is built from: the rule's policy with the
// member's own, a cache policy on its Service, written over it
func resolveMember(opts *kubecfg.Options, rule, member *ir.Policy) effective {
	if member == nil {
		return resolve(opts, rule)
	}
	var base ir.Policy
	if rule != nil {
		base = *rule
	}
	merged := base.Overlay(member)
	return resolve(opts, &merged)
}

func resolve(opts *kubecfg.Options, p *ir.Policy) effective {
	e := effective{routingMode: opts.RoutingMode(), healthMode: ao.HealthModeProvider}
	if d := opts.Defaults; d != nil {
		e.cacheName = d.CacheName
		e.negativeCacheName = d.NegativeCacheName
		e.tracingName = d.TracingName
		e.rewriterName = d.ReqRewriterName
		e.authenticatorName = d.AuthenticatorName
		e.timeout = time.Duration(d.Timeout)
		e.accessLog = d.AccessLog
		e.healthCheck = d.HealthCheck
		if d.HealthMode != "" {
			e.healthMode = d.HealthMode
		}
	}
	if p == nil {
		return e
	}
	e.policy = p
	if p.RoutingMode != "" {
		e.routingMode = p.RoutingMode
	}
	if p.HealthMode != "" {
		e.healthMode = p.HealthMode
	}
	if p.CacheName != "" {
		e.cacheName = p.CacheName
	}
	if p.NegativeCacheName != "" {
		e.negativeCacheName = p.NegativeCacheName
	}
	if p.TimeoutMS > 0 {
		e.timeout = time.Duration(p.TimeoutMS) * time.Millisecond
	}
	if p.Handler != "" {
		e.handlerName = p.Handler
	}
	e.tsProvider = p.Provider
	// the operator-tier names: no annotation sets them, so a policy carrying one came from
	// the configuration's own hands, such as a GatewayClass's parameters
	if p.TracingName != "" {
		e.tracingName = p.TracingName
	}
	if p.ReqRewriterName != "" {
		e.rewriterName = p.ReqRewriterName
	}
	if p.AuthenticatorName != "" {
		e.authenticatorName = p.AuthenticatorName
	}
	return e
}

func (e effective) handler() string {
	if e.handlerName != "" {
		return e.handlerName
	}
	if e.cacheName != "" || e.tsProvider != "" {
		return handlerProxyCache
	}
	return handlerProxy
}

func (e effective) caches() bool {
	// the reverse-proxy provider registers only the proxy handler, and a path naming a handler its
	// provider lacks is silently skipped at registration: the route would vanish, not go uncached
	return e.tsProvider != "" || e.handler() == handlerProxyCache
}

func (e effective) provider() string {
	if e.tsProvider != "" {
		return e.tsProvider
	}
	if e.caches() {
		return providers.ReverseProxyCacheShort
	}
	return providers.ReverseProxyShort
}

func (e effective) hidesResult() bool {
	return e.policy != nil && e.policy.ResultHeader == ir.ResultHeaderHide
}

func hideResult(paths []*pathDoc, e effective) {
	if !e.hidesResult() {
		return
	}
	for _, p := range paths {
		p.HideResultHeader = true
	}
}

func (e effective) probeHealthCheck() *ho.Options {
	hc := &ho.Options{}
	if e.healthCheck != nil {
		hc = e.healthCheck.Clone()
	}
	if hc.Interval <= 0 {
		hc.Interval = timeconv.Duration(kubecfg.DefaultProbeInterval)
	}
	return hc
}
