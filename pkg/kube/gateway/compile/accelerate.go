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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
)

// ProviderPaths returns the paths a time series provider predefines, handlers and cache key
// components included; the compiler emits them itself, so a provider's paths never reach a
// listener except through what a route awarded, and never without the route's policy
type ProviderPaths func(provider string) po.List

// ErrNoProviderPaths indicates a rule selected a time series provider and the compiler was given
// no source of its paths; serving the provider without them would silently accelerate nothing
var ErrNoProviderPaths = errors.New("no source of predefined paths for a time series provider")

func (i index) providerPaths(e effective) (po.List, error) {
	if e.tsProvider == "" {
		return nil, nil
	}
	if i.providers == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoProviderPaths, e.tsProvider)
	}
	return i.providers(e.tsProvider), nil
}

// accelerated is one predefined path a rule's entry reaches, for the methods both allow
type accelerated struct {
	def     *po.Options
	entry   entry
	methods []string
}

// accelerateAll decides, before any rule is compiled, which provider paths each directly served
// terminal accelerates, and fills the methods it did not award at each of them, so those
// methods resolve as if the path did not exist rather than as a 405 from the router
func (p *planner) accelerateAll(idx index, opts *kubecfg.Options) error {
	for _, name := range p.order {
		rs := p.rules[name]
		if rs.rule.Redirect() != nil || len(rs.group.Members) != 1 || rs.group.Members[0].Invalid {
			continue
		}
		m := rs.group.Members[0]
		meff := resolveMember(opts, idx.policy(rs.rule), idx.named(m.Policy))
		if meff.routingMode != kubecfg.RoutingModeService {
			// an endpoint-mode front is an ALB; its template takes every provider path
			continue
		}
		defaults, err := idx.providerPaths(meff)
		if err != nil {
			return err
		}
		if len(defaults) == 0 {
			continue
		}
		list := p.accelerate(rs, p.terminalEntries(rs), defaults)
		p.accel[name] = list
		p.fillAccelerated(name, rs, list)
	}
	return nil
}

// accelerate returns the provider paths the terminal's own entries reach: each predefined path
// below a prefix the planner awarded, on every listener and host the route names, unless a
// slot more specific than the prefix owns it there; the path carries the entry's conditions
func (p *planner) accelerate(rs *ruleState, entries []entry, defaults po.List) []accelerated {
	var out []accelerated
	for _, e := range entries {
		// a fill is another slot's leftover, and only a prefix reaches below itself
		if e.matchIdx < 0 || e.path.Type != ir.PathPrefix {
			continue
		}
		for _, def := range defaults {
			if def == nil || def.Path == "/" || def.Path == e.path.Value ||
				!coversPath(e.path.Value, def.Path) || !p.owns(rs, e.path.Value, def.Path) {
				continue
			}
			ms := intersectMethods(methods.Expand(e.methods), providerMethods(def))
			if len(ms) == 0 {
				continue
			}
			out = append(out, accelerated{def: def, entry: e, methods: methods.Compact(ms)})
		}
	}
	return out
}

// owns reports whether the prefix is the most specific slot covering the path on every listener
// and host the rule is served on: no route declared the path itself, and no longer prefix covers it
func (p *planner) owns(rs *ruleState, prefix, path string) bool {
	hosts := rs.route.Hostnames
	if len(hosts) == 0 {
		hosts = []string{""}
	}
	for _, l := range rs.listeners {
		for _, h := range hosts {
			if _, declared := p.slots[slot{
				listener: l, host: h, pathType: ir.PathExact,
				path: path,
			}]; declared {
				return false
			}
			for _, cand := range p.tiers[tierKey{listener: l, host: h}] {
				if !coversPath(cand.path, path) {
					continue
				}
				if cand.path != prefix {
					return false
				}
				break
			}
		}
	}
	return true
}

// fillAccelerated registers, for every method an accelerated path does not serve, the entry
// the covering prefix would have resolved that method to: the terminal's own claimant, another
// route's on the same frontage, or the 404 responder, exactly as a declared slot's leftovers
func (p *planner) fillAccelerated(name string, rs *ruleState, list []accelerated) {
	hosts := rs.route.Hostnames
	if len(hosts) == 0 {
		hosts = []string{""}
	}
	for _, a := range list {
		awarded := methods.Expand(a.methods)
		for _, l := range rs.listeners {
			for _, h := range hosts {
				ps := slot{listener: l, host: h, pathType: ir.PathExact, path: a.def.Path}
				ss := slot{listener: l, host: h, pathType: ir.PathPrefix, path: a.entry.path.Value}
				p.fillSlot(name, ps, ss, awarded)
			}
		}
	}
}

func (p *planner) fillSlot(name string, ps, ss slot, awarded []string) {
	type key struct {
		name   string
		cover  *claimant
		shared bool
	}
	targets := make(map[key]*fill)
	var order []key
	add := func(k key, m string) {
		f, ok := targets[k]
		if !ok {
			f = &fill{slot: ps, cover: k.cover, shared: k.shared}
			targets[k] = f
			order = append(order, k)
		}
		f.methods = append(f.methods, m)
	}
	for _, m := range methods.AllHTTPMethods() {
		if slices.Contains(awarded, m) {
			continue
		}
		cands := p.resolve(ss).byMethod[m]
		if len(cands) == 0 {
			cands = p.sameTierCover(ss, m)
		}
		// the terminal's own claimants may take the entry whatever else they serve, since the
		// accelerated path is already on it; another's must serve only this frontage
		own := len(cands) > 0 && !slices.ContainsFunc(cands, func(c *claimant) bool {
			return c.terminal() != name
		})
		if len(cands) == 0 || (!own && slices.ContainsFunc(cands, func(c *claimant) bool {
			return !sameFrontage(c, ps)
		})) {
			add(key{name: p.notfoundFor(ps)}, m)
			continue
		}
		for _, c := range cands {
			add(key{name: c.terminal(), cover: c, shared: len(cands) > 1}, m)
		}
	}
	for _, k := range order {
		p.fills[k.name] = append(p.fills[k.name], *targets[k])
	}
}

func coversPath(prefix, path string) bool {
	if prefix == "/" {
		return path != "/"
	}
	_, ok := reqmatching.CutPathPrefix(path, prefix)
	return ok
}

// providerMethods returns the concrete methods a predefined path serves; a path naming none
// serves GET alone, as path options read an empty list
func providerMethods(def *po.Options) []string {
	if len(def.Methods) == 0 {
		return []string{http.MethodGet}
	}
	return methods.Expand(def.Methods)
}

func intersectMethods(a, b []string) []string {
	out := make([]string, 0, len(a))
	for _, m := range a {
		if slices.Contains(b, m) {
			out = append(out, m)
		}
	}
	return out
}

func acceleratedPaths(list []accelerated, rewriters map[int]string, over *pathOverrides,
	hide bool,
) []*pathDoc {
	out := make([]*pathDoc, 0, len(list))
	for _, a := range list {
		d := providerPath(a.def, a.methods, rewriters[a.entry.matchIdx], over, hide)
		d.MatchHeaders = conditions(a.entry.headers)
		d.MatchQueryParams = conditions(a.entry.queries)
		d.MatchOrder = a.entry.order
		out = append(out, d)
	}
	return out
}

// providerCatchAll returns every predefined path with the member's settings, for a backend
// reached only through a dispatch that already restricted what reaches it
func providerCatchAll(defaults po.List, rewriter string, over *pathOverrides,
	hide bool,
) []*pathDoc {
	out := make([]*pathDoc, 0, len(defaults))
	for _, def := range defaults {
		if def == nil {
			continue
		}
		out = append(out, providerPath(def, methods.Compact(providerMethods(def)), rewriter,
			over, hide))
	}
	return out
}

// providerPath projects one predefined path with the route's settings over it: the provider's
// handler and its own cache key components are kept, since they are what accelerates, and the
// policy's components, header updates, CORS, collapsed forwarding and result header join them
func providerPath(def *po.Options, ms []string, rewriter string, over *pathOverrides,
	hide bool,
) *pathDoc {
	d := &pathDoc{
		Path:                    def.Path,
		MatchType:               string(def.MatchTypeName),
		Handler:                 def.HandlerName,
		Methods:                 ms,
		CacheKeyParams:          unionNames(def.CacheKeyParams, over.keyParams(), strings.EqualFold),
		CacheKeyHeaders:         unionNames(def.CacheKeyHeaders, over.keyHeaders(), strings.EqualFold),
		CacheKeyFormFields:      slices.Clone(def.CacheKeyFormFields),
		RequestHeaders:          foldHeaders(def.RequestHeaders, over.request()),
		ResponseHeaders:         foldHeaders(def.ResponseHeaders, over.response()),
		CollapsedForwardingName: def.CollapsedForwardingName,
		NoMetrics:               def.NoMetrics,
		ReqRewriterName:         rewriter,
		HideResultHeader:        hide,
	}
	if d.MatchType == "" {
		d.MatchType = string(matching.PathMatchNameExact)
	}
	if over != nil {
		d.CORS = over.cors
		if over.collapsedForwarding != "" {
			d.CollapsedForwardingName = over.collapsedForwarding
		}
		over.engine(d)
	}
	return d
}

// unionNames returns the first list with the second's names that it lacks appended, nil when
// both are empty
func unionNames(a, b []string, equal func(string, string) bool) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.ContainsFunc(out, func(have string) bool { return equal(have, n) }) {
			out = append(out, n)
		}
	}
	return out
}

func foldHeaders(base, over map[string]string) map[string]string {
	if len(base)+len(over) == 0 {
		return nil
	}
	var u headers.Updates
	u.Merge(base)
	u.Merge(over)
	return u.Render()
}
