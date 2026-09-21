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

package cachepolicy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	netv1 "k8s.io/api/networking/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Config carries what the index judges policies against
type Config struct {
	// Known is what the running configuration defines that a policy may name; a nil set
	// accepts any name
	Known ir.ConfiguredNames
	// Exists reports whether a target object is in the watched scope; nil treats every
	// target as present
	Exists func(kind, namespace, name string) bool
	// ProviderPaths returns the paths a time series provider predefines, which a route served
	// through the provider may not declare itself; nil checks none
	ProviderPaths func(provider string) []string
}

// Index holds every policy read, lowered and judged, and answers which one governs a target
type Index struct {
	cfg      Config
	entries  []*entry
	byTarget map[string]*entry
	problems *translate.Problems
}

// entry is one policy: its lowering, or why it could not be lowered, and the verdict on each
// of its targets
type entry struct {
	src     ir.Source
	policy  ir.Policy
	invalid string
	targets []ir.AncestorStatus
}

// Reasons a target is not governed
var (
	errNoTargets       = errors.New("targetRefs names no target")
	errTargetKind      = errors.New("targetRef kind is not supported")
	errTargetGroup     = errors.New("targetRef group does not match its kind")
	errTargetName      = errors.New("targetRef names no object")
	errTargetSection   = errors.New("sectionName is not supported for this kind")
	errTargetRepeated  = errors.New("targetRef is named more than once")
	errTargetNotFound  = errors.New("targetRef is not found in a watched namespace")
	errTargetConflicts = errors.New("targetRef is already governed by the older policy")
)

// groups the target kinds belong to
var kindGroups = map[string]string{
	KindGateway:   gwapiv1.GroupName,
	KindHTTPRoute: gwapiv1.GroupName,
	KindService:   "",
	KindIngress:   netv1.GroupName,
}

// sectioned is the kinds a sectionName narrows: a route to one rule, a Service to one port
var sectioned = map[string]bool{KindHTTPRoute: true, KindService: true}

// New indexes policies oldest first, so of two governing one target the older wins; every
// policy is judged, and Report describes each
func New(policies []*CachePolicy, cfg Config) *Index {
	x := &Index{
		cfg:      cfg,
		byTarget: make(map[string]*entry),
		problems: translate.NewProblems(true),
	}
	for _, p := range translate.ByAge(policies) {
		x.add(p)
	}
	return x
}

func (x *Index) add(p *CachePolicy) {
	src := translate.Source(Kind, p)
	e := &entry{src: src}
	x.entries = append(x.entries, e)
	policy, err := x.lower(p)
	if err != nil {
		// nothing of an invalid policy applies: a route governed by half a policy would
		// look configured and be something else
		e.invalid = err.Error()
		x.problems.RejectAs(string(gwapiv1.PolicyReasonInvalid), src, "%s", e.invalid)
	}
	policy.Name, policy.Source = src.Key(), src
	e.policy = policy
	if len(p.Spec.TargetRefs) == 0 {
		x.problems.RejectAs(string(gwapiv1.PolicyReasonInvalid), src, "%s", errNoTargets)
	}
	seen := make(map[string]struct{}, len(p.Spec.TargetRefs))
	for _, ref := range p.Spec.TargetRefs {
		e.targets = append(e.targets, x.target(e, p.Namespace, ref, seen))
	}
}

func (x *Index) target(e *entry, namespace string, ref TargetRef,
	seen map[string]struct{},
) ir.AncestorStatus {
	out := ir.AncestorStatus{Ref: ir.ParentRef{
		Group: ref.Group, Kind: ref.Kind, Namespace: namespace,
		Name: ref.Name, SectionName: ref.SectionName,
	}}
	refuse := func(reason gwapiv1.PolicyConditionReason, err error) ir.AncestorStatus {
		msg := fmt.Sprintf("targetRef %s %s: %s", ref.Kind, ref.Name, err)
		x.problems.RejectAs(string(reason), e.src, "%s", msg)
		out.Conditions = []ir.Condition{accepted(false, reason, msg)}
		return out
	}
	if e.invalid != "" {
		out.Conditions = []ir.Condition{accepted(false, gwapiv1.PolicyReasonInvalid, e.invalid)}
		return out
	}
	group, ok := kindGroups[ref.Kind]
	if !ok {
		return refuse(gwapiv1.PolicyReasonInvalid, errTargetKind)
	}
	if ref.Group != "" && ref.Group != group {
		return refuse(gwapiv1.PolicyReasonInvalid,
			fmt.Errorf("%w (%s is served by %q)", errTargetGroup, ref.Kind, group))
	}
	if ref.Name == "" {
		return refuse(gwapiv1.PolicyReasonInvalid, errTargetName)
	}
	if ref.SectionName != "" && !sectioned[ref.Kind] {
		return refuse(gwapiv1.PolicyReasonInvalid, errTargetSection)
	}
	key := targetKey(ref.Kind, namespace, ref.Name, ref.SectionName)
	if _, dup := seen[key]; dup {
		return refuse(gwapiv1.PolicyReasonInvalid, errTargetRepeated)
	}
	seen[key] = struct{}{}
	if x.cfg.Exists != nil && !x.cfg.Exists(ref.Kind, namespace, ref.Name) {
		return refuse(gwapiv1.PolicyReasonTargetNotFound, errTargetNotFound)
	}
	if owner, taken := x.byTarget[key]; taken {
		return refuse(gwapiv1.PolicyReasonConflicted,
			fmt.Errorf("%w %s", errTargetConflicts, owner.src.Key()))
	}
	x.byTarget[key] = e
	out.Conditions = []ir.Condition{accepted(true, gwapiv1.PolicyReasonAccepted,
		"the policy governs the target")}
	return out
}

func accepted(status bool, reason gwapiv1.PolicyConditionReason, msg string) ir.Condition {
	return ir.Condition{
		Type: string(gwapiv1.PolicyConditionAccepted), Status: status,
		Reason: string(reason), Message: msg,
	}
}

func targetKey(kind, namespace, name, section string) string {
	return strings.Join([]string{kind, namespace, name, section}, "/")
}

// field is one spec field and the parse that writes it onto the lowered policy
type field struct {
	name string
	set  func() error
}

func (x *Index) lower(p *CachePolicy) (ir.Policy, error) {
	var out ir.Policy
	s := p.Spec
	fields := []field{
		{"handler", parse(&out.Handler, s.Handler, translate.Handler)},
		{"provider", parse(&out.Provider, s.Provider, translate.Provider)},
		{"cacheName", x.known(&out.CacheName, s.CacheName, x.cfg.Known.Caches, "cache")},
		{"negativeCacheName", x.known(&out.NegativeCacheName, s.NegativeCacheName,
			x.cfg.Known.NegativeCaches, "negative cache")},
		{"maxTTL", duration(&out.MaxTTLMS, s.MaxTTL)},
		{"timeout", duration(&out.TimeoutMS, s.Timeout)},
		{"collapsedForwarding", parse(&out.CollapsedForwarding, s.CollapsedForwarding,
			translate.CollapsedForwarding)},
		{"cacheKeyParams", list(&out.CacheKeyParams, s.CacheKeyParams, translate.ParamNames)},
		{"cacheKeyHeaders", list(&out.CacheKeyHeaders, s.CacheKeyHeaders, translate.HeaderNames)},
		{"requestHeaders", headerMap(&out.RequestHeaders, s.RequestHeaders)},
		{"responseHeaders", headerMap(&out.ResponseHeaders, s.ResponseHeaders)},
		{"healthMode", parse(&out.HealthMode, s.HealthMode, translate.HealthMode)},
		{"loadBalancing", parse(&out.LoadBalancing, s.LoadBalancing, translate.LoadBalancing)},
		{"loadBalancingKey", parse(&out.LoadBalancingKey, s.LoadBalancingKey, translate.LoadBalancingKey)},
		{"resultHeader", parse(&out.ResultHeader, s.ResultHeader, translate.ResultHeader)},
	}
	if s.CORS != nil {
		fields = append(fields,
			field{"cors.mode", parse(&out.CORSMode, s.CORS.Mode, translate.CORSMode)},
			field{"cors.headers", headerMap(&out.CORSHeaders, s.CORS.Headers)})
	}
	for _, f := range fields {
		if err := f.set(); err != nil {
			return ir.Policy{}, fmt.Errorf("spec.%s: %w", f.name, err)
		}
	}
	return out, nil
}

func parse(dst *string, v string, fn func(string) (string, error)) func() error {
	return func() error {
		if v == "" {
			return nil
		}
		parsed, err := fn(v)
		if err != nil {
			return err
		}
		*dst = parsed
		return nil
	}
}

func (x *Index) known(dst *string, v string, known interface{ Contains(string) bool },
	kind string,
) func() error {
	return func() error {
		if v == "" {
			return nil
		}
		if known != nil && !known.Contains(v) {
			return fmt.Errorf("no %s named %q is configured", kind, v)
		}
		*dst = v
		return nil
	}
}

func duration(dst *int64, v string) func() error {
	return func() error {
		if v == "" {
			return nil
		}
		d, err := timeconv.ParsePositiveDuration(v)
		if err != nil {
			return err
		}
		*dst = d.Milliseconds()
		return nil
	}
}

func list(dst *[]string, v []string, fn func([]string) ([]string, error)) func() error {
	return func() error {
		parsed, err := fn(v)
		if err != nil {
			return err
		}
		*dst = parsed
		return nil
	}
}

func headerMap(dst *map[string]string, v map[string]string) func() error {
	return func() error {
		parsed, err := translate.HeaderMap(v)
		if err != nil {
			return err
		}
		*dst = parsed
		return nil
	}
}

// Lookup returns the policy governing a target, false when none does; a section-narrowed
// target is looked up under its section only, so the caller asks for the whole object as well
func (x *Index) Lookup(kind, namespace, name, section string) (*ir.Policy, bool) {
	if x == nil {
		return nil, false
	}
	e, ok := x.byTarget[targetKey(kind, namespace, name, section)]
	if !ok {
		return nil, false
	}
	p := e.policy.Clone()
	return &p, true
}

// ProviderConflict returns a path the policy's provider predefines among the matches, which the
// provider's own handler would otherwise lose to the route's; the root, which every provider
// predefines as a plain proxy catch-all, is never a conflict
func (x *Index) ProviderConflict(p *ir.Policy, matches []ir.Match) (string, bool) {
	if x == nil || p == nil || p.Provider == "" || x.cfg.ProviderPaths == nil {
		return "", false
	}
	reserved := x.cfg.ProviderPaths(p.Provider)
	for _, m := range matches {
		if m.Path.Value == "/" {
			continue
		}
		if slices.Contains(reserved, m.Path.Value) {
			return m.Path.Value, true
		}
	}
	return "", false
}

// Problems returns what the index could not do, for logging and Events
func (x *Index) Problems() []ir.Problem {
	if x == nil {
		return nil
	}
	return x.problems.List()
}

// Report returns one status entry per policy read, in age order
func (x *Index) Report() []ir.PolicyStatus {
	if x == nil {
		return nil
	}
	out := make([]ir.PolicyStatus, 0, len(x.entries))
	for _, e := range x.entries {
		out = append(out, ir.PolicyStatus{Source: e.src, Ancestors: slices.Clone(e.targets)})
	}
	return out
}
