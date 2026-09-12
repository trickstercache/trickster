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
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
)

// The planner decides which router entries each generated backend carries: precedence among matches
// meeting at one slot becomes their match order, and an unclaimed method on a claimed path is filled.

// slot is one router entry short of its method: a listener's router, a host
// tier and a path in one of its match tiers
type slot struct {
	listener string
	host     string
	pathType string
	path     string
}

func (s slot) compare(o slot) int {
	if c := strings.Compare(s.listener, o.listener); c != 0 {
		return c
	}
	if c := strings.Compare(s.host, o.host); c != 0 {
		return c
	}
	if c := strings.Compare(s.pathType, o.pathType); c != 0 {
		return c
	}
	return strings.Compare(s.path, o.path)
}

// claimant is one match of one rule asking for a slot on every listener and
// host its route names
type claimant struct {
	route     *ir.Route
	listeners []string
	ruleIdx   int
	matchIdx  int
	rule      ir.Rule
	group     ir.BackendGroup
	match     ir.Match
	methods   []string
	// method marks a match that named a method, which ranks ahead of one
	// that did not
	method bool
	// rank is the claimant's position in the precedence order of every claimant; any subset
	// meeting at a slot sorts correctly by it, so it is the match order a shared entry carries
	rank int
	// slots are the router entries the claimant asks for, in a stable order
	slots []slot
}

func (c *claimant) plain() bool {
	return len(c.match.Headers)+len(c.match.QueryParams) == 0
}

func (c *claimant) terminal() string { return GroupName(c.group) }

func (c *claimant) serves(method string) bool {
	return slices.Contains(c.methods, method)
}

func (c *claimant) compare(o *claimant) int {
	// exact and prefix paths never meet at one slot, since the router keeps them
	// in separate tiers.
	if c.method != o.method {
		return boolOrder(c.method)
	}
	if h, oh := len(c.match.Headers), len(o.match.Headers); h != oh {
		return oh - h
	}
	if q, oq := len(c.match.QueryParams), len(o.match.QueryParams); q != oq {
		return oq - q
	}
	if c.route.Rank != o.route.Rank {
		return c.route.Rank - o.route.Rank
	}
	if n := strings.Compare(c.route.Name, o.route.Name); n != 0 {
		return n
	}
	if c.ruleIdx != o.ruleIdx {
		return c.ruleIdx - o.ruleIdx
	}
	return c.matchIdx - o.matchIdx
}

func boolOrder(first bool) int {
	if first {
		return -1
	}
	return 1
}

// slotPlan is a slot fully resolved: per method, the candidates the router tries in order, ending
// at the first unconditioned one because it matches everything; and the methods no claimant serves
type slotPlan struct {
	byMethod map[string][]*claimant
	leftover []string
}

// fill is a router entry registered on a backend other than the declaring match's, so an
// unclaimed method resolves as if the path did not exist: the covering candidates, or a 404 responder
type fill struct {
	slot    slot
	methods []string
	// cover is the covering claimant whose conditions, rank and rewriter the
	// entry carries; nil for a 404 fill
	cover *claimant
	// shared marks a cover that met other candidates, so the entry carries
	// its rank
	shared bool
}

func (f fill) entry() entry {
	e := entry{
		path:    ir.PathMatch{Type: f.slot.pathType, Value: f.slot.path},
		methods: methods.Compact(f.methods), matchIdx: -1,
	}
	if f.cover != nil {
		e.matchIdx = f.cover.matchIdx
		e.headers, e.queries = f.cover.match.Headers, f.cover.match.QueryParams
		if f.shared {
			e.order = f.cover.order()
		}
	}
	return e
}

// notfound is the fixed 404 responder for one listener and host tier
type notfound struct {
	listener string
	host     string
}

type planner struct {
	idx   index
	slots map[slot][]*claimant
	rules map[string]*ruleState
	order []string
	memo  map[slot]*slotPlan
	// fills are keyed by the backend that receives the entry
	fills    map[string][]fill
	notfound map[string]*notfound
	// tiers indexes each listener and host tier's prefix slots, longest
	// first, and covers memoizes each slot's ordered covers
	tiers     map[tierKey][]slot
	coverMemo map[slot][]slot
	// accel holds the provider paths each directly served terminal accelerates
	accel map[string][]accelerated
}

// tierKey is one host tier of one listener's router
type tierKey struct{ listener, host string }

// ruleState is one rule's claimants, in match order
type ruleState struct {
	route     *ir.Route
	rule      ir.Rule
	group     ir.BackendGroup
	listeners []string
	claimants []*claimant
}

func newPlanner(idx index) *planner {
	return &planner{
		idx:       idx,
		slots:     make(map[slot][]*claimant),
		rules:     make(map[string]*ruleState),
		memo:      make(map[slot]*slotPlan),
		fills:     make(map[string][]fill),
		notfound:  make(map[string]*notfound),
		tiers:     make(map[tierKey][]slot),
		coverMemo: make(map[slot][]slot),
		accel:     make(map[string][]accelerated),
	}
}

func matchesOf(rule ir.Rule) []ir.Match {
	if len(rule.Matches) > 0 {
		return rule.Matches
	}
	return []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/"}}}
}

func (p *planner) collect(model *ir.IR) {
	// a route attached to no listener asks for nothing
	var all []*claimant
	for ri := range model.Routes {
		r := &model.Routes[ri]
		listeners := p.idx.listenerNames(*r)
		if len(listeners) == 0 {
			continue
		}
		hosts := r.Hostnames
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		for k, rule := range r.Rules {
			group, ok := p.idx.groups[rule.BackendGroup]
			if !ok || (len(group.Members) == 0 && rule.Redirect() == nil) {
				// a redirecting rule dispatches to no member, so its group
				// may be empty; any other rule with none has nothing to say
				continue
			}
			name := GroupName(group)
			rs := &ruleState{route: r, rule: rule, group: group, listeners: listeners}
			p.rules[name] = rs
			p.order = append(p.order, name)
			for i, m := range matchesOf(rule) {
				c := &claimant{
					route: r, listeners: listeners, ruleIdx: k, matchIdx: i,
					rule: rule, group: group, match: m,
					methods: methods.Expand(m.Methods), method: m.MethodSpecific,
				}
				for _, l := range listeners {
					for _, h := range hosts {
						s := slot{listener: l, host: h, pathType: m.Path.Type, path: m.Path.Value}
						c.slots = append(c.slots, s)
						p.slots[s] = append(p.slots[s], c)
					}
				}
				rs.claimants = append(rs.claimants, c)
				all = append(all, c)
			}
		}
	}
	slices.SortStableFunc(all, (*claimant).compare)
	for i, c := range all {
		c.rank = i
	}
}

func (c *claimant) order() int { return c.rank + 1 }

func (p *planner) resolve(s slot) *slotPlan {
	// ranks start at one so an ordered entry is never mistaken for an unordered
	// one
	if plan, ok := p.memo[s]; ok {
		return plan
	}
	plan := &slotPlan{byMethod: make(map[string][]*claimant)}
	claimants := slices.SortedFunc(slices.Values(p.slots[s]),
		func(a, b *claimant) int { return a.rank - b.rank })
	for _, m := range methods.AllHTTPMethods() {
		var cands []*claimant
		for _, c := range claimants {
			if !c.serves(m) {
				continue
			}
			cands = append(cands, c)
			if c.plain() {
				// the translators award a slot and method to one plain match; the first in
				// precedence order is kept should two arrive, and nothing behind it is reachable
				break
			}
		}
		if len(cands) == 0 {
			plan.leftover = append(plan.leftover, m)
			continue
		}
		plan.byMethod[m] = cands
	}
	p.memo[s] = plan
	return plan
}

func (p *planner) index() {
	for s := range p.slots {
		if s.pathType != ir.PathPrefix {
			continue
		}
		k := tierKey{listener: s.listener, host: s.host}
		p.tiers[k] = append(p.tiers[k], s)
	}
	for _, prefixes := range p.tiers {
		slices.SortFunc(prefixes, func(a, b slot) int {
			if c := len(b.path) - len(a.path); c != 0 {
				return c
			}
			return a.compare(b)
		})
	}
}

func (p *planner) covers(s slot) []slot {
	// a prefix covers on a segment boundary, as the router matches it, and regex
	// slots neither cover nor are covered
	if s.pathType == ir.PathRegex {
		return nil
	}
	if out, ok := p.coverMemo[s]; ok {
		return out
	}
	var out []slot
	for _, cand := range p.tiers[tierKey{listener: s.listener, host: s.host}] {
		if cand == s {
			continue
		}
		if _, ok := reqmatching.CutPathPrefix(s.path, cand.path); ok {
			out = append(out, cand)
		}
	}
	p.coverMemo[s] = out
	return out
}

func (p *planner) fillLeftovers() {
	type key struct {
		name   string
		cover  *claimant
		shared bool
	}
	for _, s := range p.sortedSlots() {
		plan := p.resolve(s)
		if len(plan.leftover) == 0 {
			continue
		}
		targets := make(map[key]*fill)
		var order []key
		add := func(k key, m string) {
			f, ok := targets[k]
			if !ok {
				f = &fill{slot: s, cover: k.cover, shared: k.shared}
				targets[k] = f
				order = append(order, k)
			}
			f.methods = append(f.methods, m)
		}
		for _, m := range plan.leftover {
			cands := p.sameTierCover(s, m)
			if len(cands) == 0 {
				add(key{name: p.notfoundFor(s)}, m)
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
}

func (p *planner) sameTierCover(s slot, m string) []*claimant {
	// A cover whose backends register on hosts or listeners the slot does not name
	// is skipped, since a fill on it would leak the entry there.
	for _, cand := range p.covers(s) {
		cands := p.resolve(cand).byMethod[m]
		if len(cands) == 0 {
			continue
		}
		if slices.ContainsFunc(cands, func(c *claimant) bool { return !sameFrontage(c, s) }) {
			continue
		}
		return cands
	}
	return nil
}

func sameFrontage(c *claimant, s slot) bool {
	if len(c.listeners) != 1 || c.listeners[0] != s.listener {
		return false
	}
	hosts := c.route.Hostnames
	if len(hosts) == 0 {
		return s.host == ""
	}
	return len(hosts) == 1 && hosts[0] == s.host
}

func (p *planner) notfoundFor(s slot) string {
	name := NotFoundName(s.listener, s.host)
	if _, ok := p.notfound[name]; !ok {
		p.notfound[name] = &notfound{listener: s.listener, host: s.host}
	}
	return name
}

func (p *planner) resolveAll() {
	p.index()
	for _, s := range p.sortedSlots() {
		p.resolve(s)
	}
	p.fillLeftovers()
}

func (p *planner) sortedSlots() []slot {
	out := make([]slot, 0, len(p.slots))
	for s := range p.slots {
		out = append(out, s)
	}
	slices.SortFunc(out, slot.compare)
	return out
}

// entry is one path a generated backend carries
type entry struct {
	path    ir.PathMatch
	methods []string
	// matchIdx selects the rewriter the entry runs, or -1 for none
	matchIdx int
	// headers and queries condition the entry, and order ranks it among the
	// candidates of its slot; zero leaves it unranked
	headers []ir.KeyValueMatch
	queries []ir.KeyValueMatch
	order   int
}

func (p *planner) terminalEntries(rs *ruleState) []entry {
	var out []entry
	for _, c := range rs.claimants {
		var ranked, unranked []string
		for _, m := range c.methods {
			var served, shared bool
			for _, s := range c.slots {
				cands := p.resolve(s).byMethod[m]
				if !slices.Contains(cands, c) {
					continue
				}
				served = true
				shared = shared || len(cands) > 1
			}
			switch {
			case !served:
			case shared:
				ranked = append(ranked, m)
			default:
				unranked = append(unranked, m)
			}
		}
		if len(unranked) > 0 {
			out = append(out, c.entry(unranked, 0))
		}
		if len(ranked) > 0 {
			out = append(out, c.entry(ranked, c.order()))
		}
	}
	for _, f := range p.fills[GroupName(rs.group)] {
		out = append(out, f.entry())
	}
	return out
}

func (c *claimant) entry(served []string, order int) entry {
	return entry{
		path: c.match.Path, methods: methods.Compact(served), matchIdx: c.matchIdx,
		headers: c.match.Headers, queries: c.match.QueryParams, order: order,
	}
}

func (p *planner) sortedNotFound() []string {
	out := make([]string, 0, len(p.notfound))
	for name := range p.notfound {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// NotFoundName is the generated name of the fixed 404 responder for one listener and host tier,
// answering the methods no route claimed on a path some route did claim rather than a 405
func NotFoundName(listener, host string) string {
	sum := sha256.Sum256([]byte(listener + "\x00" + host))
	return Prefix + "notfound_" + hex.EncodeToString(sum[:4])
}
