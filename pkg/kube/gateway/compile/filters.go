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
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	rwo "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

// pathOverrides is what the engine reads from the path it serves a request from; nil when
// nothing sets them. Mirrors are kept apart, since they belong to the path a request first reaches.
type pathOverrides struct {
	requestHeaders      map[string]string
	responseHeaders     map[string]string
	cors                *corsDoc
	collapsedForwarding string
	cacheKeyParams      []string
	cacheKeyHeaders     []string
	timeout             time.Duration
	attemptTimeout      time.Duration
	retry               *retryDoc
}

func overridesFor(e effective, rule ir.Rule, member []ir.Filter) *pathOverrides {
	// a URL rewrite's hostname is a Host header update: the proxy reads the upstream Host from
	// the path's request headers, and the connection still goes to the resolved Service
	var req, resp headers.Updates
	if p := e.policy; p != nil {
		req.Merge(p.RequestHeaders)
		resp.Merge(p.ResponseHeaders)
	}
	for _, filters := range [][]ir.Filter{rule.Filters, member} {
		for _, f := range filters {
			switch f.Type {
			case ir.FilterRequestHeaders:
				applyHeaderFilter(&req, f.RequestHeaders)
			case ir.FilterResponseHeaders:
				applyHeaderFilter(&resp, f.ResponseHeaders)
			case ir.FilterURLRewrite:
				if f.URLRewrite != nil && f.URLRewrite.Hostname != "" {
					req.Set(headers.NameHost, f.URLRewrite.Hostname)
				}
			}
		}
	}
	o := &pathOverrides{
		requestHeaders: req.Render(), responseHeaders: resp.Render(),
	}
	if t := rule.Timeouts; t != nil {
		o.timeout = time.Duration(t.RequestMS) * time.Millisecond
		o.attemptTimeout = time.Duration(t.BackendRequestMS) * time.Millisecond
	}
	if r := rule.Retry; r != nil {
		// the Gateway API defines no retry budget, so every eligible request is retried
		o.retry = &retryDoc{Attempts: r.Attempts, Codes: slices.Clone(r.Codes), BudgetPercent: 100}
		if r.BackoffMS > 0 {
			o.retry.Backoff = (time.Duration(r.BackoffMS) * time.Millisecond).String()
		}
	}
	if p := e.policy; p != nil {
		o.collapsedForwarding = p.CollapsedForwarding
		if p.CORSMode != "" || len(p.CORSHeaders) > 0 {
			o.cors = &corsDoc{Mode: p.CORSMode, Headers: p.CORSHeaders}
		}
		if e.caches() {
			// a cache key is only hashed by a caching path
			o.cacheKeyParams = p.CacheKeyParams
			o.cacheKeyHeaders = p.CacheKeyHeaders
		}
	}
	if len(o.requestHeaders) == 0 && len(o.responseHeaders) == 0 &&
		o.cors == nil && o.collapsedForwarding == "" &&
		len(o.cacheKeyParams) == 0 && len(o.cacheKeyHeaders) == 0 &&
		o.timeout == 0 && o.attemptTimeout == 0 && o.retry == nil {
		return nil
	}
	return o
}

func mirrorOf(filters []ir.Filter) *ir.MirrorFilter {
	for _, f := range filters {
		if f.Type == ir.FilterMirror && f.Mirror != nil {
			return f.Mirror
		}
	}
	return nil
}

func applyMirrors(paths []*pathDoc, mirrors []*mirrorDoc) {
	// the paths a request first reaches carry every mirror that applies to it
	if len(mirrors) == 0 {
		return
	}
	for _, p := range paths {
		p.Mirrors = mirrors
	}
}

// The accessors below read an override set that may be nil, which a rule with nothing to override is

func (o *pathOverrides) keyParams() []string {
	if o == nil {
		return nil
	}
	return o.cacheKeyParams
}

func (o *pathOverrides) keyHeaders() []string {
	if o == nil {
		return nil
	}
	return o.cacheKeyHeaders
}

func (o *pathOverrides) request() map[string]string {
	if o == nil {
		return nil
	}
	return o.requestHeaders
}

func (o *pathOverrides) response() map[string]string {
	if o == nil {
		return nil
	}
	return o.responseHeaders
}

func (o *pathOverrides) apply(p *pathDoc) {
	if o == nil || p == nil {
		return
	}
	p.RequestHeaders = o.requestHeaders
	p.ResponseHeaders = o.responseHeaders
	p.CORS = o.cors
	p.CollapsedForwardingName = o.collapsedForwarding
	p.CacheKeyParams = o.cacheKeyParams
	p.CacheKeyHeaders = o.cacheKeyHeaders
	o.engine(p)
}

func (o *pathOverrides) engine(p *pathDoc) {
	// the deadlines, the retry policy and trailer relaying are read by the engine alone
	if o == nil || p == nil {
		return
	}
	if o.timeout > 0 {
		p.Timeout = o.timeout.String()
	}
	if o.attemptTimeout > 0 {
		p.AttemptTimeout = o.attemptTimeout.String()
	}
	p.Retry = o.retry
}

func applyHeaderFilter(u *headers.Updates, f *ir.HeaderFilter) {
	if f == nil {
		return
	}
	for _, s := range f.Set {
		u.Set(s.Name, s.Value)
	}
	for _, a := range f.Add {
		u.Add(a.Name, a.Value)
	}
	for _, r := range f.Remove {
		u.Remove(r)
	}
}

func compileRewriters(doc *document, g ir.BackendGroup, rule ir.Rule,
	e effective, single *ir.BackendMember, listenerPort int,
) map[int]string {
	// matches whose instructions agree share one rewriter; an exact or regex match knows the whole
	// path it matched and sets it outright, a prefix match replaces only its leading segments
	var target string
	if e.policy != nil {
		target = e.policy.RewriteTarget
	}
	matches := matchesOf(rule)
	out := make(map[int]string, len(matches))
	shared := make(map[string]string, len(matches))
	for i, m := range matches {
		var instructions [][]string
		if target != "" {
			switch m.Path.Type {
			case ir.PathPrefix:
				instructions = append(instructions, rwo.PathPrefixReplace(m.Path.Value, target))
			default:
				instructions = append(instructions, rwo.PathSet(target))
			}
		}
		instructions = append(instructions, urlInstructions(m.Path, rule.Filters, listenerPort)...)
		if single != nil {
			instructions = append(instructions, urlInstructions(m.Path, single.Filters, 0)...)
		}
		if len(instructions) == 0 {
			continue
		}
		key := fmt.Sprint(instructions)
		if name, ok := shared[key]; ok {
			out[i] = name
			continue
		}
		name := RewriterName(g, i)
		doc.rewriter(name, instructions)
		shared[key] = name
		out[i] = name
	}
	return out
}

func compileMemberRewriter(doc *document, g ir.BackendGroup, m ir.BackendMember) string {
	// a prefix replacement cannot be realized here, since the member does not know which match
	// it is behind; the translators refuse one on a rule with several members
	var instructions [][]string
	for _, f := range m.Filters {
		if f.Type != ir.FilterURLRewrite || f.URLRewrite == nil || f.URLRewrite.Path == nil ||
			f.URLRewrite.Path.Type != ir.PathReplaceFull {
			continue
		}
		instructions = append(instructions, rwo.PathSet(f.URLRewrite.Path.Value))
	}
	if len(instructions) == 0 {
		return ""
	}
	name := MemberRewriterName(g, m)
	doc.rewriter(name, instructions)
	return name
}

func urlInstructions(path ir.PathMatch, filters []ir.Filter, listenerPort int) [][]string {
	// a redirect naming neither scheme nor port sends the client back to the port it arrived
	// on, which is the listener's rather than whatever the Host header claimed
	var out [][]string
	for _, f := range filters {
		switch f.Type {
		case ir.FilterURLRewrite:
			if f.URLRewrite != nil && f.URLRewrite.Path != nil {
				out = append(out, pathInstruction(path, f.URLRewrite.Path))
			}
		case ir.FilterRedirect:
			r := f.Redirect
			if r == nil {
				continue
			}
			if r.Scheme != "" {
				out = append(out, rwo.SchemeSet(r.Scheme))
			}
			if r.Hostname != "" {
				out = append(out, rwo.HostnameSet(r.Hostname))
			}
			port := r.Port
			if port == 0 && r.Scheme == "" {
				port = listenerPort
			}
			if port > 0 {
				out = append(out, rwo.PortSet(strconv.Itoa(port)))
			}
			if r.Path != nil {
				out = append(out, pathInstruction(path, r.Path))
			}
		}
	}
	return out
}

func pathInstruction(path ir.PathMatch, mod *ir.PathModifier) []string {
	// a prefix replacement matches the declared prefix on a segment boundary, so the prefix
	// itself, with or without a trailing slash, and paths below it rewrite as the API describes
	if mod.Type == ir.PathReplaceFull {
		return rwo.PathSet(mod.Value)
	}
	value := mod.Value
	if value == "" {
		value = "/"
	}
	return rwo.PathPrefixReplace(path.Value, value)
}
