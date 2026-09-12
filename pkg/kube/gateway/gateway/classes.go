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

package gateway

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The keys a GatewayClass's parameters ConfigMap may carry: the kubernetes.defaults fields,
// spelled the same way, overriding those defaults for every route served through the class
const (
	ParamRoutingMode       = "routing_mode"
	ParamCacheName         = "cache_name"
	ParamNegativeCacheName = "negative_cache_name"
	ParamTracingName       = "tracing_name"
	ParamReqRewriterName   = "req_rewriter_name"
	ParamAuthenticatorName = "authenticator_name"
	ParamTimeout           = "timeout"
	ParamHealthMode        = "health_mode"
)

var (
	errEmptyParam   = errors.New("value is empty")
	errUnknownParam = errors.New("not a recognized parameter")
)

// classState is one claimed GatewayClass: the policy its parameters produced and whether its
// Gateways are served; parameters that cannot be honored unserve it
type classState struct {
	policy string
	served bool
}

func (t *translator) claimClasses() map[string]classState {
	out := make(map[string]classState)
	for _, gc := range t.cfg.Cache.GatewayClasses() {
		if !t.cfg.Claimer.GatewayClass(gc) {
			continue
		}
		src := translate.Source(ir.KindGatewayClass, gc)
		policy, refusal := t.classPolicy(src, gc)
		out[gc.Name] = classState{policy: policy, served: refusal == ""}
		accepted := condition(gwapiv1.GatewayClassConditionStatusAccepted, true,
			gwapiv1.GatewayClassReasonAccepted, "the class is served by this controller")
		if refusal != "" {
			accepted = condition(gwapiv1.GatewayClassConditionStatusAccepted, false,
				gwapiv1.GatewayClassReasonInvalidParameters, refusal)
		}
		t.report.Classes = append(t.report.Classes, ir.ClassStatus{Source: src, Accepted: accepted})
	}
	return out
}

func (t *translator) classPolicy(src ir.Source, gc *gwapiv1.GatewayClass) (string, string) {
	// an unreadable reference or an invalid key refuses the class, since
	// parameters carry operator controls
	ref := gc.Spec.ParametersRef
	if ref == nil {
		return "", ""
	}
	refuse := func(format string, args ...any) (string, string) {
		msg := fmt.Sprintf(format, args...)
		t.problems.RejectAs(ir.ReasonInvalidParameters, src, "%s", msg)
		return "", msg
	}
	if string(ref.Group) != "" || string(ref.Kind) != kindConfigMap {
		return refuse("parametersRef kind %s/%s is not supported; only a ConfigMap is",
			ref.Group, ref.Kind)
	}
	ns := translate.NamespaceOf(ref.Namespace, "")
	if ns == "" {
		return refuse("parametersRef names no namespace")
	}
	cm := t.cfg.Cache.ConfigMap(ns, ref.Name)
	if cm == nil {
		return refuse("parametersRef ConfigMap %s/%s not found, or not in a watched namespace",
			ns, ref.Name)
	}
	p := ir.Policy{Name: src.Key(), Source: src}
	keys := slices.Sorted(maps.Keys(cm.Data))
	var rejected []string
	for _, k := range keys {
		if err := t.applyParameter(&p, k, strings.TrimSpace(cm.Data[k])); err != nil {
			t.problems.RejectAs(ir.ReasonInvalidParameters, src,
				"parameter %q rejected: %s", k, err)
			rejected = append(rejected, fmt.Sprintf("%q: %s", k, err))
		}
	}
	if len(rejected) > 0 {
		return "", "parameters rejected: " + strings.Join(rejected, "; ")
	}
	if len(keys) == 0 {
		return "", ""
	}
	return t.policies.Add(p), ""
}

// paramSetter parses one parameter value onto the policy
type paramSetter func(t *translator, p *ir.Policy, value string) error

// classParams is the parameter vocabulary: each key parses with the shared
// policy value parsers and writes one policy field
var classParams = map[string]paramSetter{
	ParamRoutingMode: func(_ *translator, p *ir.Policy, v string) (err error) {
		p.RoutingMode, err = translate.RoutingMode(v)
		return err
	},
	ParamHealthMode: func(_ *translator, p *ir.Policy, v string) (err error) {
		p.HealthMode, err = translate.HealthMode(v)
		return err
	},
	ParamTimeout: func(_ *translator, p *ir.Policy, v string) error {
		d, err := timeconv.ParsePositiveDuration(v)
		if err != nil {
			return err
		}
		p.TimeoutMS = d.Milliseconds()
		return nil
	},
	ParamCacheName: func(t *translator, p *ir.Policy, v string) error {
		return t.setKnown(&p.CacheName, v, t.known.Caches, "cache")
	},
	ParamNegativeCacheName: func(t *translator, p *ir.Policy, v string) error {
		return t.setKnown(&p.NegativeCacheName, v, t.known.NegativeCaches, "negative cache")
	},
	ParamTracingName: func(t *translator, p *ir.Policy, v string) error {
		return t.setKnown(&p.TracingName, v, t.known.Tracers, "tracing config")
	},
	ParamReqRewriterName: func(t *translator, p *ir.Policy, v string) error {
		return t.setKnown(&p.ReqRewriterName, v, t.known.Rewriters, "request rewriter")
	},
	ParamAuthenticatorName: func(t *translator, p *ir.Policy, v string) error {
		return t.setKnown(&p.AuthenticatorName, v, t.known.Authenticators, "authenticator")
	},
}

func (t *translator) applyParameter(p *ir.Policy, key, value string) error {
	if value == "" {
		return errEmptyParam
	}
	set, ok := classParams[key]
	if !ok {
		return errUnknownParam
	}
	return set(t, p, value)
}

func (t *translator) setKnown(dst *string, value string, known sets.Set[string], kind string) error {
	// an undefined one would fail validation for the whole generated configuration
	if err := translate.CheckName(known, kind, value); err != nil {
		return err
	}
	*dst = value
	return nil
}
