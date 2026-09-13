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

// Package ingress translates claimed Ingress v1 objects into the IR: listeners come from
// configuration, everything else reads the spec, and a duplicate goes to the older object
package ingress

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/annotations"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
)

// Reasons a piece of an Ingress is not translated
var (
	errEmptyPath    = errors.New("path is empty")
	errRelativePath = errors.New("path must begin with '/'")
	errBadPathType  = errors.New("unsupported pathType")
	errBadRegex     = errors.New("path is not a valid regular expression")
)

// Cache is the read side of the watch layer this translator reads; every lookup is served from
// an informer cache, so a whole translation pass makes no API calls
type Cache interface {
	translate.CoreCache
	Ingresses() []*netv1.Ingress
	IngressClasses() []*netv1.IngressClass
}

// Config carries the translator's inputs
type Config struct {
	// Cache reads the watched objects
	Cache Cache
	// Claimer decides which Ingresses belong to this controller
	Claimer *class.Claimer
	// Options is the validated kubernetes configuration section
	Options *kubecfg.Options
	// KnownNames reports what the running configuration defines that a route may name; nil
	// accepts any name
	KnownNames func() ir.ConfiguredNames
	// CertJudge judges a TLS Secret's material and reads what its certificate answers for; nil
	// parses it on every pass
	CertJudge func(*corev1.Secret) (ir.CertIdentity, error)
	// Policies are the cache policies read this pass, judged and indexed by target; nil
	// governs nothing
	Policies *cachepolicy.Index
}

// controllerSource is the source of the listeners the controller synthesizes
// for Ingress; no Kubernetes object declared them, so none is statused for them
var controllerSource = ir.Source{Kind: ir.KindController, Name: "ingress"}

// Translate builds the IR for every claimed Ingress, and the report naming
// each one so the leader can publish addresses into its status
func Translate(cfg Config) (*ir.IR, *ir.Report, []ir.Problem) {
	t := &translator{
		cfg: cfg, problems: translate.NewProblems(false),
		certs: translate.CertRefs{Judge: cfg.CertJudge},
	}
	if cfg.Cache == nil || cfg.Claimer == nil || cfg.Options == nil {
		return &ir.IR{}, &ir.Report{}, nil
	}
	var source translate.PolicySource
	if cfg.Policies != nil {
		source = cfg.Policies
	}
	t.policies = translate.NewPolicies(&t.model, source, t.problems)
	return t.run()
}

type translator struct {
	cfg      Config
	model    ir.IR
	problems *translate.Problems
	// certs records each referenced TLS Secret once, however many Ingresses name it
	certs translate.CertRefs
	// claims maps a concrete host and path identity to the Ingress that won it, keyed on what
	// the router registers rather than what was declared, since two declarations can lower alike
	claims map[string]ir.Source
	// listeners are the IR listener names every route attaches to
	listeners []string
	// known is what the running configuration defines that a route may name
	known ir.ConfiguredNames
	// policies holds every policy the model names, from annotations and cache policies, and
	// mints the combinations a rule governed by several needs
	policies *translate.Policies
}

// candidate is one concrete route a claimed Ingress asks for: one host, one
// lowered path match, and the backend behind it
type candidate struct {
	// src is the Ingress that asked for the route
	src ir.Source
	// ingress is the source Ingress's position in the age-ordered list and path the declaring
	// path's position within it; together they are the declaration order every tie falls back to
	ingress int
	path    int
	// priority orders claims ahead of age: Kubernetes gives an exact path type precedence
	// over a prefix one, a property of the rule rather than of the object's age
	priority int
	host     string
	match    ir.Match
	backend  *netv1.IngressBackend
	// won records whether this candidate kept its route, and lostTo the
	// object that took it when it did not
	won    bool
	lostTo ir.Source
}

// claim priorities; lower claims first
const (
	// priorityExact is a path declared with pathType Exact
	priorityExact = iota
	// priorityDerived is every other declared path: a prefix or a regular
	// expression
	priorityDerived
	// priorityFallback is the synthesized default backend, which answers only what nothing else
	// claimed; a declared rule lowering onto the same route wins it
	priorityFallback
)

func (t *translator) reject(s ir.Source, format string, args ...any) {
	t.problems.Reject(s, format, args...)
}

func (t *translator) run() (*ir.IR, *ir.Report, []ir.Problem) {
	claimed := t.cfg.Claimer.ClaimedIngressClasses(t.cfg.Cache.IngressClasses())
	ingresses := t.claimedIngresses(claimed)
	report := &ir.Report{}
	for _, ing := range ingresses {
		report.Ingresses = append(report.Ingresses, translate.Source(ir.KindIngress, ing))
	}
	if len(ingresses) == 0 {
		return &ir.IR{}, report, t.problems.List()
	}
	t.claims = make(map[string]ir.Source)
	if t.cfg.KnownNames != nil {
		t.known = t.cfg.KnownNames()
	}
	for _, ing := range ingresses {
		t.certRefs(ing)
	}
	t.buildListeners()

	// claiming is a pass of its own because precedence is not declaration order: an exact path
	// outranks an older prefix, so every candidate is known before any is awarded
	candidates := make([][]*candidate, len(ingresses))
	policies := make([]string, len(ingresses))
	for i, ing := range ingresses {
		candidates[i], policies[i] = t.candidates(i, ing)
	}
	t.award(candidates)
	for i, ing := range ingresses {
		t.emit(ing, candidates[i], policies[i])
	}
	return &t.model, report, t.problems.List()
}

func (t *translator) award(byIngress [][]*candidate) {
	// order is precedence first, then the age of the declaring object, then
	// declaration order, so every replica reaches the same answer.
	flat := make([]*candidate, 0, len(byIngress))
	for _, list := range byIngress {
		flat = append(flat, list...)
	}
	slices.SortStableFunc(flat, func(a, b *candidate) int {
		if c := a.priority - b.priority; c != 0 {
			return c
		}
		if c := a.ingress - b.ingress; c != 0 {
			return c
		}
		return a.path - b.path
	})
	for _, c := range flat {
		c.won, c.lostTo = t.claim(c)
	}
	// a declaration is reported only when it lost every route it asked for; an exact rule taking
	// the path itself from a prefix is the precedence Kubernetes defines, not a conflict
	for _, list := range byIngress {
		byPath := make(map[int][]*candidate, len(list))
		order := make([]int, 0, len(list))
		for _, c := range list {
			if _, ok := byPath[c.path]; !ok {
				order = append(order, c.path)
			}
			byPath[c.path] = append(byPath[c.path], c)
		}
		for _, path := range order {
			t.reportLoss(byPath[path])
		}
	}
}

func (t *translator) reportLoss(group []*candidate) {
	for _, c := range group {
		if c.won {
			return
		}
	}
	c := group[0]
	if c.priority == priorityFallback {
		// the operator did not write this path; saying they declared it
		// twice would send them looking for something that is not there
		t.reject(c.src, "the default backend is not reachable: %s already "+
			"serves every request no other rule matched", c.lostTo.Key())
		return
	}
	if c.lostTo.Key() == c.src.Key() {
		t.reject(c.src, "host %q path %q is declared more than once",
			c.host, c.match.Path.Value)
		return
	}
	t.reject(c.src, "host %q path %q is already served by %s",
		c.host, c.match.Path.Value, c.lostTo.Key())
}

func (t *translator) claimedIngresses(claimed map[string]bool) []*netv1.Ingress {
	// age decides conflicts, so it also decides processing order: the first
	// claimant of a host and path keeps it.
	all := t.cfg.Cache.Ingresses()
	out := make([]*netv1.Ingress, 0, len(all))
	for _, ing := range all {
		if t.cfg.Claimer.Ingress(ing, claimed) {
			out = append(out, ing)
		}
	}
	return translate.ByAge(out)
}

func (t *translator) buildListeners() {
	// an Ingress cannot describe a port, so the listeners are the operator's and every route
	// and certificate attaches to all of them
	refs := make([]string, 0, len(t.model.Certs))
	for _, c := range t.model.Certs {
		refs = append(refs, c.Name)
	}
	for _, name := range t.cfg.Options.Listeners() {
		t.model.Listeners = append(t.model.Listeners, ir.Listener{
			Name: name, External: true, CertRefs: refs,
			Source: controllerSource,
		})
		t.listeners = append(t.listeners, name)
	}
}

func (t *translator) certRefs(ing *netv1.Ingress) {
	// the controller watches only TLS-typed Secrets, so an absent one is missing or the wrong
	// type, and serving a listener that cannot complete a handshake is worse than saying so
	src := translate.Source(ir.KindIngress, ing)
	for _, tls := range ing.Spec.TLS {
		if tls.SecretName == "" {
			t.reject(src, "a spec.tls entry names no secret")
			continue
		}
		key, err := t.certs.Collect(t.cfg.Cache, &t.model, src, ing.Namespace, tls.SecretName)
		if err != nil {
			t.problems.RejectAs(ir.ReasonInvalidCertificate, src, "tls secret %q: %s", key, err)
		}
	}
}

func (t *translator) candidates(index int, ing *netv1.Ingress,
) ([]*candidate, string) {
	// it claims nothing; award decides that once every Ingress has been read.
	src := translate.Source(ir.KindIngress, ing)
	set, problems := annotations.Parse(ing.Annotations)
	for _, p := range problems {
		t.problems.RejectAs(ir.ReasonInvalidAnnotation, src, "%s", p.String())
	}
	t.checkNames(src, set)
	var policy string
	if set.ConfiguresPolicy() {
		p := set.Policy
		p.Name = src.Key()
		p.Source = src
		policy = t.policies.Add(p)
	}
	// a cache policy on the Ingress is written over its annotations
	policy, _ = t.policies.Merge(policy,
		t.policies.Attach(translate.TargetIngress, ing.Namespace, ing.Name, ""))

	var out []*candidate
	var pathIndex int
	for _, rule := range ing.Spec.Rules {
		host, err := translate.Hostname(rule.Host, translate.HostnameAllowEmpty)
		if err != nil {
			t.reject(src, "host %q is not routable: %s", rule.Host, err)
			continue
		}
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			matches, priority, err := t.lower(path, set.UseRegex)
			pathIndex++
			if err != nil {
				t.reject(src, "path %q dropped: %s", path.Path, err)
				continue
			}
			for _, m := range matches {
				out = append(out, &candidate{
					src: src, ingress: index, path: pathIndex,
					priority: priority, host: host, match: m,
					backend: &path.Backend,
				})
			}
		}
	}
	if ing.Spec.DefaultBackend != nil {
		// a default backend must be the last route tried; a hostless root prefix would beat a
		// hostless regex rule, so it is the shortest catch-all pattern on no host, as low as it goes
		out = append(out, &candidate{
			src: src, ingress: index, path: pathIndex + 1,
			priority: priorityFallback,
			match: ir.Match{Path: ir.PathMatch{
				Type: ir.PathRegex, Value: ir.CatchAllRegex,
			}},
			backend: ing.Spec.DefaultBackend,
		})
	}
	return out, policy
}

func (t *translator) checkNames(src ir.Source, set *annotations.Set) {
	// an undefined name would fail validation for the whole generated configuration, so one
	// object's typo would stop every route; dropping it leaves this route on the defaults
	checkName(t, src, &set.Policy.CacheName, t.known.Caches,
		annotations.CacheName, "cache")
	checkName(t, src, &set.Policy.NegativeCacheName, t.known.NegativeCaches,
		annotations.NegativeCacheName, "negative cache")
}

func checkName(t *translator, src ir.Source, name *string,
	known sets.Set[string], annotation, kind string,
) {
	if err := translate.CheckName(known, kind, *name); err != nil {
		t.problems.RejectAs(ir.ReasonInvalidAnnotation, src, "%s: %s", annotation, err)
		*name = ""
	}
}

func (t *translator) emit(ing *netv1.Ingress, candidates []*candidate,
	policy string,
) {
	src := translate.Source(ir.KindIngress, ing)
	// one counter across the whole object: its rules become one IR route per host, and every
	// generated backend name is built from this index, so it is unique within the Ingress
	var ruleIndex int
	byHost := make(map[string]*ir.Route)
	// the candidates one source path lowered into are one rule behind one
	// backend, so they are gathered back together by that path's position
	rules := make(map[int]int)
	for _, c := range candidates {
		if !c.won {
			continue
		}
		r, ok := byHost[c.host]
		if !ok {
			r = t.newRoute(src, c.host)
			byHost[c.host] = r
		}
		if at, ok := rules[c.path]; ok {
			r.Rules[at].Matches = append(r.Rules[at].Matches, c.match)
			continue
		}
		matches := []ir.Match{c.match}
		group := t.backendGroup(src, ruleIndex, ing.Namespace, c.backend, matches)
		t.model.Backends = append(t.model.Backends, group)
		r.Rules = append(r.Rules, ir.Rule{
			Matches: matches, BackendGroup: group.Name,
			Policy: t.policies.Bind(src, ruleIndex, matches, policy),
		})
		rules[c.path] = len(r.Rules) - 1
		ruleIndex++
	}
	for _, host := range slices.Sorted(maps.Keys(byHost)) {
		t.model.Routes = append(t.model.Routes, *byHost[host])
	}
}

func (t *translator) newRoute(src ir.Source, host string) *ir.Route {
	r := &ir.Route{
		Name:      src.Key() + "|" + host,
		Source:    src,
		Listeners: slices.Clone(t.listeners),
	}
	if host != "" {
		r.Hostnames = []string{host}
	}
	return r
}

func (t *translator) claim(c *candidate) (bool, ir.Source) {
	// only an identical route is a conflict: the router orders overlapping paths itself, so a
	// more specific path never needs to win here to win at request time
	key := c.host + "\x00" + c.match.Path.Type + "\x00" + c.match.Path.Value
	if owner, ok := t.claims[key]; ok {
		return false, owner
	}
	t.claims[key] = c.src
	return true, ir.Source{}
}

func (t *translator) lower(p netv1.HTTPIngressPath, useRegex bool,
) ([]ir.Match, int, error) {
	if p.Path == "" {
		return nil, 0, errEmptyPath
	}
	pathType := netv1.PathTypeImplementationSpecific
	if p.PathType != nil {
		pathType = *p.PathType
	}
	if pathType == netv1.PathTypeImplementationSpecific && useRegex {
		// the use-regex annotation settles what "implementation specific" means; the pattern is
		// anchored here so what is claimed is what the router registers (/x and ^/x are one route)
		anchored := reqmatching.AnchorStart(p.Path)
		if _, err := reqmatching.NewRegex(anchored); err != nil {
			return nil, 0, fmt.Errorf("%w: %w", errBadRegex, err)
		}
		return []ir.Match{{Path: ir.PathMatch{
			Type: ir.PathRegex, Value: anchored,
		}}}, priorityDerived, nil
	}
	if !strings.HasPrefix(p.Path, "/") {
		return nil, 0, errRelativePath
	}
	switch pathType {
	case netv1.PathTypeExact:
		return []ir.Match{{Path: ir.PathMatch{
			Type: ir.PathExact, Value: p.Path,
		}}}, priorityExact, nil
	case netv1.PathTypePrefix, netv1.PathTypeImplementationSpecific:
		return []ir.Match{{Path: ir.PathMatch{
			Type: ir.PathPrefix, Value: ir.NormalizePrefix(p.Path),
		}}}, priorityDerived, nil
	}
	return nil, 0, fmt.Errorf("%w: %q", errBadPathType, pathType)
}

func (t *translator) backendGroup(src ir.Source, ruleIndex int,
	namespace string, b *netv1.IngressBackend, matches []ir.Match,
) ir.BackendGroup {
	// an unresolvable backend still produces a member: the compiler answers it with a fixed
	// error, which tells an operator more than a route that silently disappeared
	g := ir.BackendGroup{
		Name:      fmt.Sprintf("%s|r%d", src.Key(), ruleIndex),
		Source:    src,
		RuleIndex: ruleIndex,
	}
	m := ir.BackendMember{Weight: 1}
	target, reason := t.resolveBackend(namespace, b)
	if reason != "" {
		m.Invalid = true
		m.InvalidReason = reason
		t.reject(src, "%s", reason)
	} else {
		m.Service = target
		m.Policy = t.policies.BindMember(src, ruleIndex, matches, target)
	}
	g.Members = []ir.BackendMember{m}
	return g
}

func (t *translator) resolveBackend(namespace string,
	b *netv1.IngressBackend,
) (ir.ServiceTarget, string) {
	var out ir.ServiceTarget
	if b == nil || b.Service == nil {
		if b != nil && b.Resource != nil {
			return out, fmt.Sprintf(
				"backend resource reference %q is not supported",
				b.Resource.Kind)
		}
		return out, "backend names no service"
	}
	name := b.Service.Name
	svc := t.cfg.Cache.Service(namespace, name)
	if svc == nil {
		return out, fmt.Sprintf("service %s/%s not found", namespace, name)
	}
	ref := translate.PortRef{
		Name: b.Service.Port.Name, Number: b.Service.Port.Number, Protocol: corev1.ProtocolTCP,
	}
	port, ok := translate.ServicePort(svc, ref)
	if !ok {
		return out, fmt.Sprintf("service %s/%s has no port %s", namespace, name, ref)
	}
	out = ir.ServiceTarget{
		Namespace: namespace, Name: name,
		Port: port.Port, PortName: port.Name, Scheme: ir.ProtocolHTTP,
	}
	return out, ""
}
