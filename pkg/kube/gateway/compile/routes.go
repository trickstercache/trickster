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
	"net/http"

	albnames "github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
)

func compileRoutes(doc *document, model *ir.IR, idx index,
	opts *kubecfg.Options,
) error {
	p := newPlanner(idx)
	p.collect(model)
	p.resolveAll()
	if err := p.accelerateAll(idx, opts); err != nil {
		return err
	}
	out := make(map[string]*backendDoc)
	for _, name := range p.order {
		if err := compileRule(doc, out, p, p.rules[name], opts); err != nil {
			return err
		}
	}
	compileNotFound(out, p)
	if len(out) > 0 {
		doc.Backends = out
	}
	return nil
}

func compileRule(doc *document, out map[string]*backendDoc, p *planner,
	rs *ruleState, opts *kubecfg.Options,
) error {
	r, rule, group := rs.route, rs.rule, rs.group
	eff := resolve(opts, p.idx.policy(rule)).forRoute(r)
	// the group's own rule index names the backends, so a translator numbering rules across
	// several IR routes (one per Ingress host) still names each rule's backends consistently
	name := GroupName(group)
	prefix := CacheKeyPrefix(r.Source)
	entries := p.terminalEntries(rs)
	// the terminal registers on the route's listeners and hosts only: path routing off keeps it
	// from answering under its own name, provider defaults off keep it to the paths emitted
	// here, which for a time series provider include the predefined paths the route reaches
	attach := func(b *backendDoc) {
		b.ListenerNames = rs.listeners
		b.PathRoutingDisabled = true
		b.PathDefaultsDisabled = true
		b.CacheKeyPrefix = prefix
		b.Hosts = r.Hostnames
		b.AnyHostRouting = len(r.Hostnames) == 0
	}

	if red := rule.Redirect(); red != nil {
		// a redirecting rule forwards nothing: one backend answers every match from the redirect
		// handler with the Location its rewriter composed, and still carries the operator's controls
		rewriters := compileRewriters(doc, group, rule, eff, nil, p.idx.listenerPort(*r))
		b := baseBackend(providers.ReverseProxyShort, eff)
		b.OriginURL = redirectOriginURL
		attach(b)
		b.CacheKeyPrefix = ""
		b.Paths = compilePaths(entries, handlerRedirect, rewriters,
			overridesFor(eff, ir.Rule{Filters: rule.Filters}, nil))
		for _, d := range b.Paths {
			d.ResponseCode = redirectStatus(red)
		}
		hideResult(b.Paths, eff)
		out[name] = b
		return nil
	}

	if len(group.Members) == 1 && !group.Members[0].Invalid {
		// one target serves the match directly, so its paths carry the match's rewriter and the
		// per-request overrides; nothing dispatches in between, so the member's filters join the rule's
		m := group.Members[0]
		meff := resolveMember(opts, p.idx.policy(rule), p.idx.named(m.Policy)).forRoute(r)
		t, err := newMemberTarget(doc, group, m, meff, opts)
		if err != nil {
			return err
		}
		defaults, err := p.idx.providerPaths(meff)
		if err != nil {
			return err
		}
		rewriters := compileRewriters(doc, group, rule, meff, &m, 0)
		over := overridesFor(meff, rule, m.Filters)
		// the rule's mirror and the member's both fire from the front, once per request
		mirrors, err := compileMirrors(doc, out, group, rule.Filters, &m, meff, opts, rs.listeners)
		if err != nil {
			return err
		}
		attach(t.front)
		if t.origin == nil {
			// the provider's paths join the route's own, each under the entry that reaches it,
			// so they are served exactly where and as the route is
			t.front.Paths = append(compilePaths(entries, t.frontHandler, rewriters, over),
				acceleratedPaths(p.accel[name], rewriters, over, meff.hidesResult())...)
		} else {
			// discovered members are cloned from the template, so the overrides live on its
			// catch-all path and the front's paths only dispatch; an ALB caches nothing itself
			t.front.CacheKeyPrefix = ""
			t.front.Paths = compilePaths(entries, t.frontHandler, rewriters, nil)
			t.origin.Paths = append(catchAllPaths(meff.handler(), "", over),
				providerCatchAll(defaults, "", over, meff.hidesResult())...)
			hideResult(t.origin.Paths, meff)
			out[t.originName] = t.origin
		}
		hideResult(t.front.Paths, meff)
		applyMirrors(t.front.Paths, mirrors)
		out[name] = t.front
		return nil
	}

	// several backendRefs: an ALB apportions traffic across one generated target per ref,
	// and the ALB is what the route's hosts and paths attach to
	pool := make([]*albPoolDoc, 0, len(group.Members))
	for _, m := range group.Members {
		memberName := GroupMemberName(group, m)
		var front *backendDoc
		if m.Invalid {
			// an unresolvable ref must still hold its share of the traffic and answer it,
			// rather than shifting that share onto its siblings
			front = invalidBackend()
		} else {
			meff := resolveMember(opts, p.idx.policy(rule), p.idx.named(m.Policy)).forRoute(r)
			t, err := newMemberTarget(doc, group, m, meff, opts)
			if err != nil {
				return err
			}
			defaults, err := p.idx.providerPaths(meff)
			if err != nil {
				return err
			}
			// the ALB dispatches through the member's own router, so the member needs a catch-all
			// path with the effective handler and the overrides the engine reads from the path it
			// serves, and every provider path beside it, since the dispatch restricted what arrives
			over := overridesFor(meff, rule, m.Filters)
			rewriter := compileMemberRewriter(doc, group, m)
			hide := meff.hidesResult()
			// a member's own mirror fires where its share of the traffic arrives
			mirrors, err := compileMirrors(doc, out, group, nil, &m, meff, opts, rs.listeners)
			if err != nil {
				return err
			}
			front = t.front
			if t.origin == nil {
				front.Paths = append(catchAllPaths(meff.handler(), rewriter, over),
					providerCatchAll(defaults, rewriter, over, hide)...)
				hideResult(front.Paths, meff)
			} else {
				front.Paths = catchAllPaths(t.frontHandler, "", nil)
				t.origin.Paths = append(catchAllPaths(meff.handler(), rewriter, over),
					providerCatchAll(defaults, rewriter, over, hide)...)
				hideResult(t.origin.Paths, meff)
				out[t.originName] = t.origin
			}
			applyMirrors(front.Paths, mirrors)
		}
		// a pool member registers no route of its own; it carries the ALB's listener names so
		// validation does not bind it to the default frontend and activate a port serving nothing
		front.ListenerNames = rs.listeners
		front.PathRoutingDisabled = true
		front.PathDefaultsDisabled = true
		if front.Provider != providers.ALB {
			front.CacheKeyPrefix = prefix
		}
		out[memberName] = front
		pool = append(pool, &albPoolDoc{Name: memberName, Weight: m.Weight})
	}
	alb := &backendDoc{Provider: providers.ALB}
	attach(alb)
	alb.CacheKeyPrefix = ""
	// an ALB's paths must select the alb handler (any other is silently dropped at registration);
	// the rewriter runs there, ahead of dispatch, so captures come from the match that produced them
	rewriters := compileRewriters(doc, group, rule, eff, nil, 0)
	alb.Paths = compilePaths(entries, providers.ALB, rewriters, nil)
	hideResult(alb.Paths, eff)
	// the rule's mirror fires ahead of the dispatch, once per request
	mirrors, err := compileMirrors(doc, out, group, rule.Filters, nil, eff, opts, rs.listeners)
	if err != nil {
		return err
	}
	applyMirrors(alb.Paths, mirrors)
	alb.ALB = &albDoc{Mechanism: albnames.MechanismRR, Pool: pool}
	out[name] = alb
	return nil
}

func compileMirrors(doc *document, out map[string]*backendDoc, group ir.BackendGroup,
	ruleFilters []ir.Filter, m *ir.BackendMember, eff effective, opts *kubecfg.Options,
	listeners []string,
) ([]*mirrorDoc, error) {
	// the rule's mirror is emitted where ruleFilters are given, the member's where m is; a
	// member fronting the rule alone is given both, in the order they apply
	var out2 []*mirrorDoc
	if mf := mirrorOf(ruleFilters); mf != nil {
		d, err := mirrorTarget(doc, out, MirrorName(group), group, mf, eff, opts, listeners)
		if err != nil {
			return nil, err
		}
		out2 = append(out2, d)
	}
	if m != nil {
		if mf := mirrorOf(m.Filters); mf != nil {
			d, err := mirrorTarget(doc, out, MemberMirrorName(group, *m), group, mf, eff, opts,
				listeners)
			if err != nil {
				return nil, err
			}
			out2 = append(out2, d)
		}
	}
	return out2, nil
}

func redirectStatus(r *ir.RedirectFilter) int {
	if r.StatusCode >= 300 && r.StatusCode < 400 {
		return r.StatusCode
	}
	return http.StatusFound
}

func compileNotFound(out map[string]*backendDoc, p *planner) {
	body := "no route matched"
	for _, name := range p.sortedNotFound() {
		nf := p.notfound[name]
		b := &backendDoc{
			Provider:             providers.ReverseProxyShort,
			OriginURL:            unmatchedOriginURL,
			ListenerNames:        []string{nf.listener},
			PathRoutingDisabled:  true,
			PathDefaultsDisabled: true,
		}
		if nf.host != "" {
			b.Hosts = []string{nf.host}
		} else {
			b.AnyHostRouting = true
		}
		var entries []entry
		for _, f := range p.fills[name] {
			entries = append(entries, f.entry())
		}
		b.Paths = compilePaths(entries, handlerLocalResponse, nil, nil)
		for _, d := range b.Paths {
			d.ResponseCode = http.StatusNotFound
			d.ResponseBody = &body
		}
		out[name] = b
	}
}

// anyMethod is the wildcard a path uses to accept every HTTP method; an empty list is not
// equivalent, since path options default it to GET alone
var anyMethod = []string{methods.Wildcard}

func catchAllPaths(handler, rewriter string, over *pathOverrides) []*pathDoc {
	return pathsFor("/", string(matching.PathMatchNamePrefix), handler, nil,
		rewriter, over)
}

func pathsFor(path, matchType, handler string, requested []string,
	rewriter string, over *pathOverrides,
) []*pathDoc {
	// a caching handler is split as the reverse-proxy-cache provider splits itself: only GET and HEAD
	// reach the object cache, since it would otherwise store and replay a mutating request's response
	newPath := func(handler string, methods []string) *pathDoc {
		p := &pathDoc{
			Path:            path,
			MatchType:       matchType,
			Handler:         handler,
			Methods:         methods,
			ReqRewriterName: rewriter,
		}
		over.apply(p)
		return p
	}
	if handler != handlerProxyCache {
		if len(requested) == 0 {
			// a match that names no method matches them all
			requested = anyMethod
		}
		return []*pathDoc{newPath(handler, requested)}
	}
	// the split needs the concrete set, so the wildcard is expanded here rather than at path
	// initialization; a method the proxy does not recognize is not cacheable, the safe direction
	cacheable, rest := methods.Partition(methods.Expand(requested), methods.IsCacheable)
	out := make([]*pathDoc, 0, 2)
	if len(cacheable) > 0 {
		out = append(out, newPath(handlerProxyCache, cacheable))
	}
	if len(rest) > 0 {
		out = append(out, newPath(handlerProxy, rest))
	}
	return out
}

func compilePaths(entries []entry, handler string, rewriters map[int]string,
	over *pathOverrides,
) []*pathDoc {
	out := make([]*pathDoc, 0, len(entries))
	for _, e := range entries {
		var rewriter string
		if e.matchIdx >= 0 {
			rewriter = rewriters[e.matchIdx]
		}
		docs := pathsFor(e.path.Value, pathMatchType(e.path.Type), handler,
			e.methods, rewriter, over)
		for _, d := range docs {
			d.MatchHeaders = conditions(e.headers)
			d.MatchQueryParams = conditions(e.queries)
			d.MatchOrder = e.order
		}
		out = append(out, docs...)
	}
	return out
}

func conditions(in []ir.KeyValueMatch) []*conditionDoc {
	// A value compared exactly is compared whole; a pattern is anchored so it must
	// match the whole value, as the Gateway API defines.
	if len(in) == 0 {
		return nil
	}
	out := make([]*conditionDoc, 0, len(in))
	for _, kv := range in {
		c := &conditionDoc{Name: kv.Name, Value: kv.Value, Regex: kv.Regex}
		if kv.Regex {
			c.Value = reqmatching.AnchorWhole(kv.Value)
		}
		out = append(out, c)
	}
	return out
}

func pathMatchType(t string) string {
	// an element-wise prefix is the router's segment match
	switch t {
	case ir.PathExact:
		return string(matching.PathMatchNameExact)
	case ir.PathRegex:
		return string(matching.PathMatchNameRegex)
	default:
		return string(matching.PathMatchNameSegment)
	}
}
