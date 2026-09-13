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
	"slices"
	"strconv"
	"strings"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Reasons a piece of an HTTPRoute is not translated
var (
	errRelativePath = errors.New("path must begin with '/'")
	errBadPathType  = errors.New("unsupported path match type")
	errBadRegex     = errors.New("not a valid regular expression")
)

// attachment is one listener an HTTPRoute was accepted on, and the
// hostnames the two agreed on; empty hostnames means any
type attachment struct {
	gateway   *gatewayState
	listener  *listenerState
	hostnames []string
	// plan is the route plan the attachment belongs to, set once the plan exists
	plan *routePlan
}

// frontage is the attachments sharing one served port and class policy,
// merged, so a route claims each router entry on that port once
type frontage struct {
	listeners []string
	policy    string
	// hostnames is the union of the attachments' hostnames, or empty when
	// any of them admitted every hostname
	hostnames []string
	anyHost   bool
}

// appProtocolH2C is the Service port appProtocol the Gateway API reserves for cleartext HTTP/2
const appProtocolH2C = "kubernetes.io/h2c"

func frontages(attachments []attachment) []*frontage {
	var out []*frontage
	byKey := make(map[string]*frontage)
	for _, a := range attachments {
		key := a.listener.protocol + ":" + strconv.Itoa(a.listener.port) + ":" +
			a.gateway.policy
		f, ok := byKey[key]
		if !ok {
			f = &frontage{policy: a.gateway.policy}
			byKey[key] = f
			out = append(out, f)
		}
		f.listeners = append(f.listeners, a.listener.name)
		if len(a.hostnames) == 0 {
			f.anyHost = true
			f.hostnames = nil
			continue
		}
		if !f.anyHost {
			f.hostnames = translate.Unique(append(f.hostnames, a.hostnames...))
		}
	}
	return out
}

// candidate is one router entry a route asks for: a lowered path with its
// predicates, on one served port and hostname, for the methods it declared
type candidate struct {
	src       ir.Source
	rank      int
	ruleIdx   int
	matchIdx  int
	listener  string
	host      string
	path      ir.PathMatch
	headers   []ir.KeyValueMatch
	queries   []ir.KeyValueMatch
	predicate string
	requested []string
	// method marks a match that named a method, which outranks one that did
	// not, whatever the routes' ages
	method bool
	won    []string
	lostTo ir.Source
}

func (c *candidate) compare(o *candidate) int {
	if c.method != o.method {
		return boolOrder(c.method)
	}
	if c.rank != o.rank {
		return c.rank - o.rank
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

// routePlan is one route lowered but not yet awarded; a GRPCRoute is planned in the
// HTTPRoute's shape, so hr carries either kind and src says which
type routePlan struct {
	src         ir.Source
	rank        int
	hr          *gwapiv1.HTTPRoute
	protocol    string
	filters     []ruleFilters
	units       []*routeUnit
	report      *routeReport
	attachments []attachment
}

// routeReport is an HTTPRoute's status while it is translated: one Accepted verdict per claimed
// parentRef, and one ResolvedRefs verdict for the route's backend references
type routeReport struct {
	src     ir.Source
	parents []ir.ParentStatus
	refs    ir.Condition
}

func newRouteReport(src ir.Source) *routeReport {
	return &routeReport{src: src, refs: condition(gwapiv1.RouteConditionResolvedRefs, true,
		gwapiv1.RouteReasonResolvedRefs, "every backend reference resolved")}
}

func (r *routeReport) parent(pr gwapiv1.ParentReference, accepted bool,
	reason gwapiv1.RouteConditionReason, msg string,
) {
	ref := ir.ParentRef{
		Group: translate.GroupOf(pr.Group), Kind: translate.KindOf(pr.Kind, ""),
		Namespace: translate.NamespaceOf(pr.Namespace, ""), Name: string(pr.Name),
	}
	if pr.SectionName != nil {
		ref.SectionName = string(*pr.SectionName)
	}
	if pr.Port != nil {
		ref.Port = int(*pr.Port)
	}
	r.parents = append(r.parents, ir.ParentStatus{Ref: ref, Conditions: []ir.Condition{
		condition(gwapiv1.RouteConditionAccepted, accepted, reason, msg),
	}})
}

func (r *routeReport) refuseAll(reason gwapiv1.RouteConditionReason, msg string) {
	for i := range r.parents {
		r.parents[i].Conditions = ir.Set(r.parents[i].Conditions,
			condition(gwapiv1.RouteConditionAccepted, false, reason, msg))
	}
}

func (r *routeReport) unresolved(reason gwapiv1.RouteConditionReason, msg string) {
	if !r.refs.Status {
		return
	}
	r.refs = condition(gwapiv1.RouteConditionResolvedRefs, false, reason, msg)
}

func (r *routeReport) status() ir.RouteStatus {
	out := ir.RouteStatus{Source: r.src, Parents: slices.Clone(r.parents)}
	for i := range out.Parents {
		out.Parents[i].Conditions = append(slices.Clone(out.Parents[i].Conditions), r.refs)
	}
	return out
}

// routeUnit is what becomes one IR route: the HTTPRoute on one served port
// and hostname, with the candidates each of its rules declared
type routeUnit struct {
	frontage *frontage
	host     string
	rules    [][]*candidate
}

// routeInput is one route of either kind in the HTTPRoute's shape, ranked by age together
type routeInput struct {
	*gwapiv1.HTTPRoute
	kind     string
	protocol string
	// err says why the route could not be expressed in the HTTPRoute's shape, if it could not
	err error
}

func (t *translator) buildRoutes() {
	// both kinds are ranked by age together, since they compete for the same listeners
	var inputs []routeInput
	for _, hr := range t.cfg.Cache.HTTPRoutes() {
		inputs = append(inputs, routeInput{HTTPRoute: hr, kind: ir.KindHTTPRoute})
	}
	for _, gr := range t.cfg.Cache.GRPCRoutes() {
		hr, err := grpcAsHTTP(gr)
		if err != nil {
			// the route keeps its metadata and parents so it is still claimed and told why
			hr = &gwapiv1.HTTPRoute{ObjectMeta: gr.ObjectMeta}
			hr.Spec.CommonRouteSpec = gr.Spec.CommonRouteSpec
		}
		inputs = append(inputs, routeInput{
			HTTPRoute: hr, kind: ir.KindGRPCRoute, protocol: ir.ProtocolGRPC, err: err,
		})
	}
	var plans []*routePlan
	for rank, in := range translate.ByAge(inputs) {
		if p := t.plan(rank, in); p != nil {
			plans = append(plans, p)
		}
	}
	plans = t.refuseCrossKindConflicts(plans)
	t.award(plans)
	for _, p := range plans {
		t.emit(p)
	}
}

func (t *translator) plan(rank int, in routeInput) *routePlan {
	hr := in.HTTPRoute
	src := translate.Source(in.kind, hr)
	attachments, report := t.attachments(src, hr)
	if report == nil {
		return nil
	}
	t.routeReports = append(t.routeReports, report)
	if len(attachments) == 0 {
		return nil
	}
	if in.err != nil {
		msg := fmt.Sprintf("%s; the route is not served", in.err)
		t.reject(src, "%s", msg)
		report.refuseAll(gwapiv1.RouteReasonUnsupportedValue, msg)
		return nil
	}
	filters, ok := t.lowerRuleFilters(src, hr, report)
	if !ok {
		report.refuseAll(gwapiv1.RouteReasonUnsupportedValue,
			"a filter cannot be honored; the route is not served")
		return nil
	}
	// a route counts as attached only once it is known to be served
	for _, a := range attachments {
		a.listener.attached++
	}
	p := &routePlan{
		src: src, rank: rank, hr: hr, protocol: in.protocol, filters: filters,
		report: report, attachments: attachments,
	}
	for i := range p.attachments {
		p.attachments[i].plan = p
	}
	for _, f := range frontages(attachments) {
		hosts := f.hostnames
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		for _, host := range hosts {
			u := &routeUnit{frontage: f, host: host}
			for k, rule := range hr.Spec.Rules {
				u.rules = append(u.rules, t.candidates(src, rank, f.listeners[0], host, k, rule))
			}
			p.units = append(p.units, u)
		}
	}
	return p
}

// routeConflictReason is the Accepted reason for a route refused because a route of the other
// kind, older than it, shares a listener and a hostname with it
const routeConflictReason = "RouteConflict"

// refuseCrossKindConflicts keeps, of an HTTPRoute and a GRPCRoute attached to one listener with
// intersecting hostnames, only the older, as the Gateway API defines for the two kinds
func (t *translator) refuseCrossKindConflicts(plans []*routePlan) []*routePlan {
	// plans arrive oldest first and a single-kind set is not scanned at all; a kept
	// attachment is indexed by its exact hostnames, so a plan naming one costs a lookup
	kinds := make(map[string]struct{}, 2)
	for _, p := range plans {
		kinds[p.src.Kind] = struct{}{}
	}
	if len(kinds) < 2 {
		return plans
	}
	kept := make(map[kindKey]*kindIndex)
	out := plans[:0]
	for _, p := range plans {
		other := ir.KindHTTPRoute
		if p.src.Kind == ir.KindHTTPRoute {
			other = ir.KindGRPCRoute
		}
		var conflict *routePlan
		for _, a := range p.attachments {
			if idx := kept[kindKey{a.listener.name, other}]; idx != nil {
				conflict = oldest(conflict, idx.conflict(a))
			}
		}
		if conflict == nil {
			for _, a := range p.attachments {
				k := kindKey{a.listener.name, p.src.Kind}
				if kept[k] == nil {
					kept[k] = &kindIndex{exact: make(map[string]*routePlan)}
				}
				kept[k].add(a)
			}
			out = append(out, p)
			continue
		}
		msg := fmt.Sprintf("the hostnames of %s intersect this route's on a shared listener, "+
			"and it is older; the route is not served", conflict.src.Key())
		p.report.refuseAll(routeConflictReason, msg)
		t.reject(p.src, "%s", msg)
		for _, a := range p.attachments {
			a.listener.attached--
		}
	}
	return out
}

// kindKey addresses the kept attachments of one route kind on one listener
type kindKey struct{ listener, kind string }

// kindIndex holds the kept attachments of one kind on one listener: exact hostnames by name,
// each to the oldest plan naming it, and the wildcard and unrestricted attachments as a list
type kindIndex struct {
	exact map[string]*routePlan
	wild  []attachment
}

func (i *kindIndex) add(a attachment) {
	if len(a.hostnames) == 0 {
		i.wild = append(i.wild, a)
		return
	}
	for _, h := range a.hostnames {
		if hostnames.IsWildcard(h) {
			i.wild = append(i.wild, a)
			continue
		}
		if _, ok := i.exact[h]; !ok {
			i.exact[h] = a.plan
		}
	}
}

func (i *kindIndex) conflict(a attachment) *routePlan {
	// an unrestricted or wildcard attachment is compared with everything kept; an exact
	// hostname is looked up, then compared with the wildcards alone
	var found *routePlan
	for _, o := range i.wild {
		if attachmentsIntersect(a, o) {
			found = oldest(found, o.plan)
		}
	}
	if len(a.hostnames) == 0 {
		for _, p := range i.exact {
			found = oldest(found, p)
		}
		return found
	}
	for _, h := range a.hostnames {
		if hostnames.IsWildcard(h) {
			for e, p := range i.exact {
				if _, ok := intersect(h, e); ok {
					found = oldest(found, p)
				}
			}
			continue
		}
		if p, ok := i.exact[h]; ok {
			found = oldest(found, p)
		}
	}
	return found
}

func attachmentsIntersect(a, o attachment) bool {
	if len(a.hostnames) == 0 || len(o.hostnames) == 0 {
		return true
	}
	for _, h := range a.hostnames {
		if _, ok := intersectHostnames(h, o.hostnames); ok {
			return true
		}
	}
	return false
}

func oldest(a, b *routePlan) *routePlan {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.rank < a.rank:
		return b
	}
	return a
}

func (t *translator) award(plans []*routePlan) {
	// a declaration is reported only when it lost everything it asked for
	var flat []*candidate
	for _, p := range plans {
		for _, u := range p.units {
			for _, rule := range u.rules {
				flat = append(flat, rule...)
			}
		}
	}
	slices.SortStableFunc(flat, (*candidate).compare)
	for _, c := range flat {
		c.won, c.lostTo = t.claim(c)
	}
	for _, p := range plans {
		for _, u := range p.units {
			for _, rule := range u.rules {
				t.reportLosses(rule)
			}
		}
	}
}

func (t *translator) reportLosses(rule []*candidate) {
	byMatch := make(map[int][]*candidate)
	var order []int
	for _, c := range rule {
		if _, ok := byMatch[c.matchIdx]; !ok {
			order = append(order, c.matchIdx)
		}
		byMatch[c.matchIdx] = append(byMatch[c.matchIdx], c)
	}
	for _, i := range order {
		group := byMatch[i]
		if slices.ContainsFunc(group, func(c *candidate) bool { return len(c.won) > 0 }) {
			continue
		}
		c := group[0]
		if c.lostTo.Key() == c.src.Key() {
			t.reject(c.src, "host %q path %q is declared more than once", c.host, c.path.Value)
			continue
		}
		t.reject(c.src, "host %q path %q is already served by %s", c.host, c.path.Value,
			c.lostTo.Key())
	}
}

func (t *translator) emit(p *routePlan) {
	// one counter across the whole object: every generated backend name is built from it, so it
	// has to be unique within the HTTPRoute rather than within any one of the routes it becomes
	var ruleIndex int
	var routePolicy string
	if p.src.Kind == ir.KindHTTPRoute {
		// a cache policy targets an HTTPRoute; a GRPCRoute is governed by its Gateway's and its
		// Services' policies alone
		routePolicy = t.policies.Attach(translate.TargetHTTPRoute, p.hr.Namespace, p.hr.Name, "")
	}
	for _, u := range p.units {
		r := ir.Route{
			Name:      p.src.Key() + "|" + u.frontage.listeners[0] + "|" + u.host,
			Source:    p.src,
			Listeners: slices.Clone(u.frontage.listeners),
			Rank:      p.rank,
			Protocol:  p.protocol,
		}
		if u.host != "" {
			// a Gateway API wildcard spans any number of labels, which the
			// router spells **.
			r.Hostnames = []string{hostnames.ToAnyDepth(u.host)}
		}
		for k, cands := range u.rules {
			var matches []ir.Match
			for _, c := range cands {
				if len(c.won) == 0 {
					continue
				}
				m := ir.Match{
					Path: c.path, Headers: c.headers, QueryParams: c.queries,
					MethodSpecific: c.method,
				}
				if c.method || len(c.won) < len(c.requested) {
					m.Methods = c.won
				}
				matches = append(matches, m)
			}
			if len(matches) == 0 {
				continue
			}
			// a rule is governed, least specific first, by its Gateway's policy, the route's, and
			// one naming the rule by its name
			var rulePolicy string
			if n := p.hr.Spec.Rules[k].Name; n != nil && *n != "" && p.src.Kind == ir.KindHTTPRoute {
				rulePolicy = t.policies.Attach(translate.TargetHTTPRoute, p.hr.Namespace,
					p.hr.Name, string(*n))
			}
			governing := []string{u.frontage.policy, routePolicy, rulePolicy}
			rf := p.filters[k]
			rf.rule = t.dropUnreachableMirrors(p.src, rf.rule, t.routingModeOf(governing...), p.report)
			group := t.backendGroup(p.src, ruleIndex, p.hr.Namespace, p.hr.Spec.Rules[k], rf,
				p.report, matches, governing)
			t.model.Backends = append(t.model.Backends, group)
			r.Rules = append(r.Rules, ir.Rule{
				Matches: matches, BackendGroup: group.Name,
				Policy:   t.policies.Bind(p.src, k, matches, governing...),
				Filters:  rf.rule,
				Timeouts: rf.timeouts,
				Retry:    rf.retry,
			})
			ruleIndex++
		}
		if len(r.Rules) > 0 {
			t.model.Routes = append(t.model.Routes, r)
		}
	}
}

// claimedParent is one parentRef naming a Gateway this controller claims
type claimedParent struct {
	pr gwapiv1.ParentReference
	gw *gatewayState
	ns string
}

func (t *translator) claimedParents(src ir.Source, hr *gwapiv1.HTTPRoute) []claimedParent {
	// a Gateway this controller does not claim draws no complaint
	var out []claimedParent
	for _, pr := range hr.Spec.ParentRefs {
		group := gwapiv1.GroupName
		if pr.Group != nil && *pr.Group != "" {
			group = string(*pr.Group)
		}
		kind := translate.KindOf(pr.Kind, kindGateway)
		if group != gwapiv1.GroupName || kind != kindGateway {
			t.reject(src, "parentRef %q: kind %s/%s is not supported", pr.Name, group, kind)
			continue
		}
		ns := translate.NamespaceOf(pr.Namespace, hr.Namespace)
		gw, claimed := t.gateways[translate.NamespacedName(ns, string(pr.Name))]
		if !claimed {
			continue
		}
		out = append(out, claimedParent{pr: pr, gw: gw, ns: ns})
	}
	return out
}

func (t *translator) attachments(src ir.Source, hr *gwapiv1.HTTPRoute,
) ([]attachment, *routeReport) {
	parents := t.claimedParents(src, hr)
	if len(parents) == 0 {
		return nil, nil
	}
	report := newRouteReport(src)
	hostnames, ok := t.routeHostnames(src, hr)
	if !ok {
		for _, p := range parents {
			report.parent(p.pr, false, gwapiv1.RouteReasonUnsupportedValue,
				"a hostname is not routable; the route is not served")
		}
		return nil, report
	}
	var out []attachment
	seen := make(map[string]struct{})
	for _, p := range parents {
		pr, gw, ns := p.pr, p.gw, p.ns
		if !gw.served {
			msg := fmt.Sprintf("parentRef %s/%s: %s", ns, pr.Name, gw.refusal)
			report.parent(pr, false, gwapiv1.RouteReasonNoMatchingParent, msg)
			t.reject(src, "%s", msg)
			continue
		}
		var candidates []*listenerState
		for _, l := range gw.listeners {
			if pr.SectionName != nil && l.section != string(*pr.SectionName) {
				continue
			}
			if pr.Port != nil && l.port != int(*pr.Port) {
				continue
			}
			candidates = append(candidates, l)
		}
		if len(candidates) == 0 {
			msg := fmt.Sprintf("parentRef %s/%s: no listener matches %s", ns, pr.Name,
				parentSelector(pr))
			report.parent(pr, false, gwapiv1.RouteReasonNoMatchingParent, msg)
			t.reject(src, "%s", msg)
			continue
		}
		var accepted, refused, unmatched int
		for _, l := range candidates {
			if !l.allowed.admits(src.Kind, gw.src.Namespace, hr.Namespace,
				t.cfg.Cache.Namespace(hr.Namespace)) {
				refused++
				continue
			}
			hosts, ok := intersectHostnames(l.hostname, hostnames)
			if !ok {
				unmatched++
				continue
			}
			accepted++
			if _, dup := seen[l.name]; dup {
				continue
			}
			seen[l.name] = struct{}{}
			out = append(out, attachment{gateway: gw, listener: l, hostnames: hosts})
		}
		switch {
		case accepted > 0:
			report.parent(pr, true, gwapiv1.RouteReasonAccepted, "the route is attached")
		case refused > 0:
			msg := fmt.Sprintf("parentRef %s/%s: no listener allows routes from namespace %q",
				ns, pr.Name, hr.Namespace)
			report.parent(pr, false, gwapiv1.RouteReasonNotAllowedByListeners, msg)
			t.reject(src, "%s", msg)
		default:
			msg := fmt.Sprintf("parentRef %s/%s: no listener hostname intersects the route's",
				ns, pr.Name)
			report.parent(pr, false, gwapiv1.RouteReasonNoMatchingListenerHostname, msg)
			t.reject(src, "%s", msg)
		}
	}
	return out, report
}

func parentSelector(pr gwapiv1.ParentReference) string {
	var parts []string
	if pr.SectionName != nil {
		parts = append(parts, "sectionName "+strconv.Quote(string(*pr.SectionName)))
	}
	if pr.Port != nil {
		parts = append(parts, "port "+strconv.Itoa(int(*pr.Port)))
	}
	if len(parts) == 0 {
		return "any listener"
	}
	return strings.Join(parts, " and ")
}

func (t *translator) routeHostnames(src ir.Source, hr *gwapiv1.HTTPRoute) ([]string, bool) {
	// one the router cannot register fails the whole route rather than widening it
	out := make([]string, 0, len(hr.Spec.Hostnames))
	for _, h := range hr.Spec.Hostnames {
		n, err := translate.Hostname(string(h), translate.HostnameRequired)
		if err != nil {
			t.reject(src, "hostname %q is not routable: %s; the route is not served", h, err)
			return nil, false
		}
		out = append(out, n)
	}
	return translate.Unique(out), true
}

func (t *translator) candidates(src ir.Source, rank int, listener, host string,
	ruleIdx int, rule gwapiv1.HTTPRouteRule,
) []*candidate {
	// a rule with no matches is a prefix match on the root
	declared := rule.Matches
	if len(declared) == 0 {
		declared = []gwapiv1.HTTPRouteMatch{{}}
	}
	var out []*candidate
	for i, m := range declared {
		paths, err := lowerPath(m.Path)
		if err != nil {
			t.reject(src, "rule %d match %d: path dropped: %s", ruleIdx, i, err)
			continue
		}
		headers, err := lowerHeaders(m.Headers)
		if err != nil {
			t.reject(src, "rule %d match %d: header match dropped: %s", ruleIdx, i, err)
			continue
		}
		queries, err := lowerQueryParams(m.QueryParams)
		if err != nil {
			t.reject(src, "rule %d match %d: query parameter match dropped: %s",
				ruleIdx, i, err)
			continue
		}
		requested := methods.AllHTTPMethods()
		var method bool
		if m.Method != nil && *m.Method != "" {
			requested = []string{strings.ToUpper(string(*m.Method))}
			method = true
		}
		key := predicateKey(headers, queries)
		for _, p := range paths {
			out = append(out, &candidate{
				src: src, rank: rank, ruleIdx: ruleIdx, matchIdx: i,
				listener: listener, host: host, path: p,
				headers: headers, queries: queries, predicate: key,
				requested: requested, method: method,
			})
		}
	}
	return out
}

func (t *translator) claim(c *candidate) ([]string, ir.Source) {
	// only an identical entry is a conflict, since the router orders the rest
	var won []string
	var lostTo ir.Source
	var lost bool
	for _, m := range c.requested {
		key := strings.Join([]string{
			c.listener, c.host, c.path.Type, c.path.Value,
			m, c.predicate,
		}, "\x00")
		if owner, taken := t.claims[key]; taken {
			if !lost {
				lost, lostTo = true, owner
			}
			continue
		}
		t.claims[key] = c.src
		won = append(won, m)
	}
	return won, lostTo
}

func lowerPath(pm *gwapiv1.HTTPPathMatch) ([]ir.PathMatch, error) {
	// A match naming no path is a prefix match on the root.
	typ, value := gwapiv1.PathMatchPathPrefix, "/"
	if pm != nil {
		if pm.Type != nil {
			typ = *pm.Type
		}
		if pm.Value != nil {
			value = *pm.Value
		}
	}
	switch typ {
	case gwapiv1.PathMatchRegularExpression:
		// the pattern is anchored here rather than at path initialization, so what is claimed
		// is what the router registers: an unanchored /x and an anchored ^/x are one route
		anchored := reqmatching.AnchorStart(value)
		if _, err := reqmatching.NewRegex(anchored); err != nil {
			return nil, fmt.Errorf("%w: %w", errBadRegex, err)
		}
		return []ir.PathMatch{{Type: ir.PathRegex, Value: anchored}}, nil
	case gwapiv1.PathMatchExact, gwapiv1.PathMatchPathPrefix:
	default:
		return nil, fmt.Errorf("%w: %q", errBadPathType, typ)
	}
	if !strings.HasPrefix(value, "/") {
		return nil, errRelativePath
	}
	if typ == gwapiv1.PathMatchExact {
		return []ir.PathMatch{{Type: ir.PathExact, Value: value}}, nil
	}
	return []ir.PathMatch{{Type: ir.PathPrefix, Value: ir.NormalizePrefix(value)}}, nil
}

func lowerHeaders(in []gwapiv1.HTTPHeaderMatch) ([]ir.KeyValueMatch, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]ir.KeyValueMatch, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, h := range in {
		name := strings.ToLower(string(h.Name))
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		kv := ir.KeyValueMatch{Name: string(h.Name), Value: h.Value}
		if h.Type != nil && *h.Type == gwapiv1.HeaderMatchRegularExpression {
			if _, err := reqmatching.NewRegex(h.Value); err != nil {
				return nil, fmt.Errorf("header %q: %w: %w", h.Name, errBadRegex, err)
			}
			kv.Regex = true
		}
		out = append(out, kv)
	}
	return out, nil
}

func lowerQueryParams(in []gwapiv1.HTTPQueryParamMatch) ([]ir.KeyValueMatch, error) {
	// a later entry for a name already conditioned is ignored, as for headers
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]ir.KeyValueMatch, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, q := range in {
		if _, dup := seen[string(q.Name)]; dup {
			continue
		}
		seen[string(q.Name)] = struct{}{}
		kv := ir.KeyValueMatch{Name: string(q.Name), Value: q.Value}
		if q.Type != nil && *q.Type == gwapiv1.QueryParamMatchRegularExpression {
			if _, err := reqmatching.NewRegex(q.Value); err != nil {
				return nil, fmt.Errorf("query parameter %q: %w: %w", q.Name, errBadRegex, err)
			}
			kv.Regex = true
		}
		out = append(out, kv)
	}
	return out, nil
}

func predicateKey(headers, queries []ir.KeyValueMatch) string {
	parts := make([]string, 0, len(headers)+len(queries))
	for _, h := range headers {
		parts = append(parts, "h:"+strings.ToLower(h.Name)+"="+h.Value+
			":"+strconv.FormatBool(h.Regex))
	}
	for _, q := range queries {
		parts = append(parts, "q:"+q.Name+"="+q.Value+":"+strconv.FormatBool(q.Regex))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x01")
}

func (t *translator) backendGroup(src ir.Source, ruleIndex int, namespace string,
	rule gwapiv1.HTTPRouteRule, rf ruleFilters, report *routeReport, matches []ir.Match,
	governing []string,
) ir.BackendGroup {
	// an unresolvable ref keeps its slot and weight as an invalid member that answers with an
	// error; a redirecting rule dispatches nowhere, so its group has no members
	g := ir.BackendGroup{
		Name:      fmt.Sprintf("%s|r%d", src.Key(), ruleIndex),
		Source:    src,
		RuleIndex: ruleIndex,
	}
	if rf.redirects() {
		if len(rule.BackendRefs) > 0 {
			t.reject(src, "rule %d: backendRefs are ignored on a redirecting rule", ruleIndex)
		}
		return g
	}
	for j, ref := range rule.BackendRefs {
		weight := 1
		if ref.Weight != nil {
			weight = int(*ref.Weight)
		}
		if weight <= 0 {
			// a zero weight is a reference that receives no traffic
			continue
		}
		m := ir.BackendMember{RefIndex: j, Weight: weight}
		if j < len(rf.refs) {
			m.Filters = rf.refs[j]
		}
		target, tls, reason, why := t.resolveBackendRef(src.Kind, namespace, ref.BackendObjectReference)
		var policy, mode string
		if reason == "" {
			// the member's own policy is the last word on the routing mode, which decides
			// whether the target can be reached at all
			policy = t.policies.BindMember(src, ruleIndex, matches, target)
			mode = t.routingModeOf(append(slices.Clone(governing), policy)...)
			reason, why = unreachable(target, mode)
		}
		if reason != "" {
			m.Invalid = true
			m.InvalidReason = reason
			t.reject(src, "backendRef %d: %s", j, reason)
			report.unresolved(why, fmt.Sprintf("backendRef %d: %s", j, reason))
		} else {
			m.Service = target
			m.TLS = tls
			m.Policy = policy
			m.Filters = t.dropUnreachableMirrors(src, m.Filters, mode, report)
		}
		g.Members = append(g.Members, m)
	}
	if len(g.Members) == 0 {
		reason := "rule has no backendRefs"
		if len(rule.BackendRefs) > 0 {
			reason = "every backendRef has weight 0"
		}
		t.reject(src, "rule %d: %s; it answers with an error", ruleIndex, reason)
		g.Members = []ir.BackendMember{{Weight: 1, Invalid: true, InvalidReason: reason}}
	}
	return g
}

func (t *translator) resolveBackendRef(routeKind, routeNS string,
	ref gwapiv1.BackendObjectReference,
) (ir.ServiceTarget, *ir.BackendTLS, string, gwapiv1.RouteConditionReason) {
	// A policy that cannot be honored makes the reference invalid, since
	// connecting without the verification it asked for is worse.
	out, reason, why := t.resolveService(routeKind, routeNS, ref, corev1.ProtocolTCP)
	if reason != "" {
		return out, nil, reason, why
	}
	name := out.Name
	ns := out.Namespace
	tls, reason, governed := t.backendTLS(ns, name, out.PortName)
	if !governed {
		return out, nil, "", ""
	}
	if reason != "" {
		return out, nil, fmt.Sprintf("service %s/%s: %s", ns, name, reason),
			gwapiv1.RouteReasonUnsupportedProtocol
	}
	out.Scheme = ir.ProtocolHTTPS
	return out, tls, "", ""
}

func (t *translator) resolveService(routeKind, routeNS string, ref gwapiv1.BackendObjectReference,
	transport corev1.Protocol,
) (ir.ServiceTarget, string, gwapiv1.RouteConditionReason) {
	// the reference must name a Service port of the route's transport, in a namespace the route
	// may reach; a port of the other transport would be a target the route cannot carry to
	var out ir.ServiceTarget
	group, kind := translate.GroupOf(ref.Group), translate.KindOf(ref.Kind, kindService)
	if group != "" || kind != kindService {
		return out, fmt.Sprintf("kind %s/%s is not supported", group, kind),
			gwapiv1.RouteReasonInvalidKind
	}
	ns, name := translate.NamespaceOf(ref.Namespace, routeNS), string(ref.Name)
	if ns != routeNS && !t.grants.permits(
		reference{group: gwapiv1.GroupName, kind: routeKind, namespace: routeNS},
		reference{group: "", kind: kindService, namespace: ns, name: name}) {
		return out, fmt.Sprintf("service %s/%s is not permitted by any ReferenceGrant",
			ns, name), gwapiv1.RouteReasonRefNotPermitted
	}
	if ref.Port == nil {
		return out, fmt.Sprintf("service %s/%s: no port named", ns, name),
			gwapiv1.RouteReasonBackendNotFound
	}
	svc := t.cfg.Cache.Service(ns, name)
	if svc == nil {
		return out, fmt.Sprintf("service %s/%s not found", ns, name),
			gwapiv1.RouteReasonBackendNotFound
	}
	portRef := translate.PortRef{Number: *ref.Port, Protocol: transport}
	port, ok := translate.ServicePort(svc, portRef)
	if !ok {
		return out, fmt.Sprintf("service %s/%s has no %s port %s", ns, name,
			strings.ToLower(string(transport)), portRef), gwapiv1.RouteReasonBackendNotFound
	}
	out = ir.ServiceTarget{
		Namespace: ns, Name: name,
		Port: port.Port, PortName: port.Name, Scheme: ir.ProtocolHTTP,
		H2C: port.AppProtocol != nil && *port.AppProtocol == appProtocolH2C,
	}
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		// a headless Service resolves to its pods, which listen on the target port; one named
		// rather than numbered is known only to the pods, so the routing mode decides its fate
		switch {
		case port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal > 0:
			out.Port = port.TargetPort.IntVal
		case port.TargetPort.Type == intstr.String && port.TargetPort.StrVal != "":
			out.TargetPortName = port.TargetPort.StrVal
		}
	}
	return out, "", ""
}

// routingModeOf returns the routing mode the named policies, least specific first, leave in force
// over the configured default, which is how the compiler resolves it for a member they govern
func (t *translator) routingModeOf(names ...string) string {
	mode := t.cfg.Options.RoutingMode()
	for _, n := range names {
		if p := t.policies.Get(n); p != nil && p.RoutingMode != "" {
			mode = p.RoutingMode
		}
	}
	return mode
}

// unreachable explains why the routing mode cannot reach the target, or nothing when it can:
// service routing dials the Service's name, which a headless Service's named target port is not on
func unreachable(target ir.ServiceTarget, mode string) (string, gwapiv1.RouteConditionReason) {
	if target.TargetPortName == "" || mode != kubecfg.RoutingModeService {
		return "", ""
	}
	return fmt.Sprintf("headless service %s/%s names its target port %q, which service routing "+
			"cannot resolve without its pods", target.Namespace, target.Name, target.TargetPortName),
		gwapiv1.RouteReasonUnsupportedValue
}

// dropUnreachableMirrors drops the mirrors whose Service the routing mode cannot reach, lowering
// ResolvedRefs as for a mirror that did not resolve; the filters are returned as they were otherwise
func (t *translator) dropUnreachableMirrors(src ir.Source, filters []ir.Filter, mode string,
	report *routeReport,
) []ir.Filter {
	var out []ir.Filter
	for i, f := range filters {
		if f.Type != ir.FilterMirror || f.Mirror == nil {
			continue
		}
		reason, why := unreachable(f.Mirror.Service, mode)
		if reason == "" {
			continue
		}
		if out == nil {
			out = slices.Clone(filters)
		}
		out[i].Mirror = nil
		t.reject(src, "requestMirror: %s; the mirror is not applied", reason)
		report.unresolved(why, "requestMirror: "+reason)
	}
	if out == nil {
		return filters
	}
	return dropUnresolvedMirrors(out)
}
