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

// Package gateway translates claimed Gateway API objects (GatewayClass, Gateway, HTTPRoute,
// GRPCRoute, TCPRoute, TLSRoute, UDPRoute, ReferenceGrant, BackendTLSPolicy) into the IR under the
// Gateway API's route precedence
package gateway

import (
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	corev1 "k8s.io/api/core/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// Cache is the read side of the watch layer this translator reads; every lookup is served from
// an informer cache, so a whole translation pass makes no API calls
type Cache interface {
	GatewayClasses() []*gwapiv1.GatewayClass
	Gateways() []*gwapiv1.Gateway
	HTTPRoutes() []*gwapiv1.HTTPRoute
	GRPCRoutes() []*gwapiv1.GRPCRoute
	TCPRoutes() []*gwapiv1a2.TCPRoute
	TLSRoutes() []*gwapiv1a2.TLSRoute
	UDPRoutes() []*gwapiv1a2.UDPRoute
	ReferenceGrants() []*gwapiv1.ReferenceGrant
	BackendTLSPolicies() []*gwapiv1.BackendTLSPolicy
	translate.CoreCache
	ConfigMap(namespace, name string) *corev1.ConfigMap
	Namespace(name string) *corev1.Namespace
}

// Config carries the translator's inputs
type Config struct {
	// Cache reads the watched objects
	Cache Cache
	// Claimer decides which GatewayClasses, and so which Gateways, belong
	// to this controller
	Claimer *class.Claimer
	// Options is the validated kubernetes configuration section
	Options *kubecfg.Options
	// KnownNames reports what the running configuration defines that a
	// GatewayClass's parameters may name. Nil accepts any name.
	KnownNames func() ir.ConfiguredNames
	// CertJudge judges a TLS Secret's material and reads what its certificate answers for; nil
	// parses it on every pass
	CertJudge func(*corev1.Secret) (ir.CertIdentity, error)
	// CertServing answers what serves under a Secret on a listener when its material is unusable,
	// since a store keeps what it holds; nil means nothing does
	CertServing func(ir.Listener, string) (ir.CertIdentity, bool)
	// Policies are the cache policies read this pass, judged and indexed by target; nil
	// governs nothing
	Policies *cachepolicy.Index
}

// Problem is one thing the translator could not do
type Problem = ir.Problem

// Translate builds the IR for every claimed Gateway and the HTTPRoutes attached to them, and the
// status report describing what became of every claimed object
func Translate(cfg Config) (*ir.IR, *ir.Report, []Problem) {
	if cfg.Cache == nil || cfg.Claimer == nil || cfg.Options == nil {
		return &ir.IR{}, &ir.Report{}, nil
	}
	t := &translator{
		cfg:      cfg,
		certs:    translate.CertRefs{Judge: cfg.CertJudge, Serving: cfg.CertServing},
		problems: translate.NewProblems(true),
		gateways: make(map[string]*gatewayState),
		claims:   make(map[string]ir.Source),
	}
	var source translate.PolicySource
	if cfg.Policies != nil {
		source = cfg.Policies
	}
	t.policies = translate.NewPolicies(&t.model, source, t.problems)
	return t.run()
}

type translator struct {
	cfg   Config
	model ir.IR
	// problems deduplicates: one rule resolved once per listener and
	// hostname would otherwise report one unresolvable reference many times
	problems *translate.Problems
	// known is what the running configuration defines that parameters may
	// name
	known ir.ConfiguredNames
	// policies holds every policy the model names, from class parameters and cache policies,
	// and mints the combinations a rule governed by several needs
	policies *translate.Policies
	// grants answers whether a cross-namespace reference is permitted
	grants *grantIndex
	// tlsPolicies answers which BackendTLSPolicy governs a Service port
	tlsPolicies *backendTLSIndex
	// classes holds every claimed GatewayClass: the policy its parameters
	// produced, and whether its Gateways are served at all
	classes map[string]classState
	// gateways holds every claimed Gateway by namespace/name, with the
	// listeners that were accepted
	gateways map[string]*gatewayState
	// certs records each referenced TLS Secret once, however many listeners name it
	certs translate.CertRefs
	// claims maps a router entry the translator has awarded to the route
	// that owns it, so a duplicate can be refused and reported
	claims map[string]ir.Source
	// report is what is written back to the claimed objects
	report ir.Report
	// gatewayOrder holds every claimed Gateway, served or not, oldest first, so the report
	// lists them in one order however the cache yielded them
	gatewayOrder []*gatewayState
	// routeReports holds one status entry per HTTPRoute naming a claimed Gateway
	routeReports []*routeReport
}

// gatewayState is one claimed Gateway and what survived of its listeners
type gatewayState struct {
	src ir.Source
	// policy names the IR policy the Gateway's class parameters produced,
	// or nothing
	policy string
	// served is false for a Gateway of a class whose parameters cannot be honored, and refusal
	// says so for the routes naming it
	served  bool
	refusal string
	// listeners are the accepted listeners, in declaration order
	listeners []*listenerState
	// status is the Gateway's status entry, completed once every route has attached
	status *ir.GatewayStatus
}

func (t *translator) reject(s ir.Source, format string, args ...any) {
	t.problems.Reject(s, format, args...)
}

func (t *translator) run() (*ir.IR, *ir.Report, []Problem) {
	if t.cfg.KnownNames != nil {
		t.known = t.cfg.KnownNames()
	}
	t.grants = indexGrants(t.cfg.Cache.ReferenceGrants())
	t.tlsPolicies = t.indexBackendTLS()
	t.classes = t.claimClasses()
	if len(t.classes) == 0 {
		return &ir.IR{}, &t.report, t.problems.List()
	}
	t.buildGateways()
	t.buildRoutes()
	t.buildStreamRoutes()
	t.finish()
	return &t.model, &t.report, t.problems.List()
}

func (t *translator) finish() {
	for _, gw := range t.gatewayOrder {
		for _, l := range gw.listeners {
			gw.status.Listeners[l.statusIdx].AttachedRoutes = l.attached
		}
		if gw.served {
			gw.status.Conditions = gatewayConditions(len(gw.status.Listeners), len(gw.listeners))
		}
		t.report.Gateways = append(t.report.Gateways, *gw.status)
	}
	for _, r := range t.routeReports {
		t.report.Routes = append(t.report.Routes, r.status())
	}
}
