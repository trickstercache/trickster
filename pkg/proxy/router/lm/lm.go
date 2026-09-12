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

// package lm represents a simple Longest Match router
package lm

import (
	"cmp"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	meth "github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/route"
)

var _ router.Router = &lmRouter{}

type lmRouter struct {
	matchScheme router.MatchingScheme
	routes      route.HostRouteSetLookup
	// wildcards holds *.suffix patterns and anyDepth **.suffix, both keyed by
	// suffix and apart from routes so an exact-host lookup is untouched
	wildcards route.HostRouteSetLookup
	anyDepth  route.HostRouteSetLookup
}

func NewRouter() router.Router {
	return &lmRouter{
		matchScheme: router.DefaultMatchingScheme,
		routes:      make(route.HostRouteSetLookup),
		wildcards:   make(route.HostRouteSetLookup),
		anyDepth:    make(route.HostRouteSetLookup),
	}
}

var (
	emptyHost      = []string{""}
	defaultMethods = []string{http.MethodGet, http.MethodHead}
)

func (rt *lmRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.RequestURI == "*" {
		if r.ProtoAtLeast(1, 1) {
			w.Header().Set(headers.NameConnection, headers.ValueClose)
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rt.Handler(r).ServeHTTP(w, r)
}

func (rt *lmRouter) RegisterRoute(path string, hosts, methods []string,
	matchType matching.PathMatchType, handler http.Handler,
) error {
	return rt.register(path, hosts, methods, matchType, nil, 0, handler)
}

func (rt *lmRouter) RegisterRouteSpec(spec route.Spec) error {
	predicates := spec.Predicates
	if predicates.Empty() {
		predicates = nil
	}
	return rt.register(spec.Path, spec.Hosts, spec.Methods, spec.MatchType,
		predicates, spec.Order, spec.Handler)
}

func (rt *lmRouter) register(path string, hosts, methods []string,
	matchType matching.PathMatchType, predicates *reqmatching.Predicates, order int,
	handler http.Handler,
) error {
	pl := len(path)
	if pl == 0 {
		return errors.ErrInvalidPath
	}
	if len(methods) == 0 {
		methods = defaultMethods
	} else {
		for i, m := range methods {
			if !meth.IsValidMethod(m) {
				return errors.ErrInvalidMethod
			}
			methods[i] = strings.ToUpper(m)
		}
	}
	var re *regexp.Regexp
	switch matchType {
	case matching.PathMatchTypeExact, matching.PathMatchTypePrefix, matching.PathMatchTypeSegment:
	case matching.PathMatchTypeRegex:
		var err error
		if re, err = regexp.Compile(path); err != nil {
			return fmt.Errorf("%w: %w", errors.ErrInvalidPath, err)
		}
	default:
		return errors.ErrInvalidMatchType
	}
	if hosts == nil {
		hosts = emptyHost
	}
	for _, h := range hosts {
		lookup, h, err := rt.hostLookup(h)
		if err != nil {
			return err
		}
		hrc, ok := lookup[h]
		if !ok || hrc == nil {
			hrc = &route.HostRouteSet{
				ExactMatchRoutes:     make(route.LookupLookup),
				PrefixMatchRoutes:    make(route.PrefixRouteSets, 0, 16),
				PrefixMatchRoutesLkp: make(route.PrefixRouteSetLookup),
				RegexMatchRoutesLkp:  make(route.RegexRouteSetLookup),
			}
			lookup[h] = hrc
		}
		spec := routeSpec{host: h, path: path, handler: handler, predicates: predicates, order: order}
		switch matchType {
		case matching.PathMatchTypeExact:
			rl, ok := hrc.ExactMatchRoutes[path]
			if rl == nil || !ok {
				rl = make(route.Lookup)
				hrc.ExactMatchRoutes[path] = rl
			}
			spec.exact = true
			registerMethods(rl, methods, spec)
		case matching.PathMatchTypePrefix, matching.PathMatchTypeSegment:
			// a segment and a plain prefix on one path are one route set, since
			// they are one path to the method rules; the boundary is per route
			prc, ok := hrc.PrefixMatchRoutesLkp[path]
			if prc == nil || !ok {
				prc = &route.PrefixRouteSet{
					Path:           path,
					PathLen:        pl,
					RoutesByMethod: make(route.Lookup),
				}
				hrc.PrefixMatchRoutesLkp[path] = prc
				if len(hrc.PrefixMatchRoutes) == 0 {
					hrc.PrefixMatchRoutes = make(route.PrefixRouteSets, 0, 16)
				}
				hrc.PrefixMatchRoutes = append(hrc.PrefixMatchRoutes, prc)
			}
			spec.exact = true
			spec.segment = matchType == matching.PathMatchTypeSegment
			hrc.HasSegments = hrc.HasSegments || spec.segment
			registerMethods(prc.RoutesByMethod, methods, spec)
			// a registration can replace the last plain route of a set, so the
			// set's own boundary requirement is recomputed rather than accumulated
			prc.Plain = hasPlainRoute(prc.RoutesByMethod)
		case matching.PathMatchTypeRegex:
			rrs, ok := hrc.RegexMatchRoutesLkp[path]
			if rrs == nil || !ok {
				rrs = &route.RegexRouteSet{
					Pattern:        path,
					PatternLen:     pl,
					Regexp:         re,
					RoutesByMethod: make(route.Lookup),
				}
				hrc.RegexMatchRoutesLkp[path] = rrs
				hrc.RegexMatchRoutes = append(hrc.RegexMatchRoutes, rrs)
			}
			registerMethods(rrs.RoutesByMethod, methods, spec)
		}
	}
	rt.sort()
	return nil
}

func (rt *lmRouter) hostLookup(h string) (route.HostRouteSetLookup, string, error) {
	// a wildcard is keyed by its suffix, a precise host by itself
	h, err := hostnames.Normalize(h, hostnames.AllowEmpty)
	if err != nil {
		return nil, "", errors.ErrInvalidHost
	}
	switch {
	case hostnames.IsAnyDepth(h):
		return rt.anyDepth, hostnames.Suffix(h), nil
	case hostnames.IsWildcard(h):
		return rt.wildcards, hostnames.Suffix(h), nil
	}
	return rt.routes, h, nil
}

// routeSpec is what one registration contributes to each method slot it names
type routeSpec struct {
	host, path string
	exact      bool
	segment    bool
	handler    http.Handler
	predicates *reqmatching.Predicates
	order      int
}

func (s routeSpec) route(method string, implicit bool) *route.Route {
	return &route.Route{
		ExactMatch: s.exact,
		Method:     method,
		Host:       s.host,
		Path:       s.path,
		Handler:    s.handler,
		Predicates: s.predicates,
		Order:      s.order,
		Segment:    s.segment,
		Implicit:   implicit,
	}
}

func registerMethods(rl route.Lookup, methods []string, spec routeSpec) {
	// every GET registration contributes an implicit HEAD alongside it
	for _, m := range methods {
		addRoute(rl, m, spec.route(m, false))
		if m == http.MethodGet {
			addRoute(rl, http.MethodHead, spec.route(http.MethodHead, true))
		}
	}
}

func hasPlainRoute(rl route.Lookup) bool {
	for _, rt := range rl {
		if rt.Candidates == nil {
			if !rt.Segment {
				return true
			}
			continue
		}
		for _, c := range rt.Candidates {
			if !c.Segment {
				return true
			}
		}
	}
	return false
}

func addRoute(rl route.Lookup, method string, r *route.Route) {
	// an unconditioned route replaces one of its own boundary kind; anything
	// else joins the slot as one more ordered candidate
	cur := rl[method]
	if cur != nil {
		// a declared HEAD supersedes the implicit HEAD of a GET registration
		// and is never joined by one: GET alone must not answer HEAD
		switch {
		case r.Implicit && !cur.Implicit:
			return
		case !r.Implicit && cur.Implicit:
			cur = nil
		}
	}
	switch {
	case cur == nil:
		if r.Predicates == nil {
			rl[method] = r
			return
		}
		rl[method] = candidateSlot(r, r)
	case cur.Candidates != nil:
		cur.Candidates = insertCandidate(cur.Candidates, r)
	case r.Predicates == nil && cur.Segment == r.Segment:
		rl[method] = r
	default:
		rl[method] = candidateSlot(cur, cur)
		rl[method].Candidates = insertCandidate(rl[method].Candidates, r)
	}
}

func insertCandidate(candidates []*route.Route, r *route.Route) []*route.Route {
	// an unconditioned route replaces the one of its own boundary kind rather
	// than joining it, which would leave the first shadowing the second
	if r.Predicates == nil {
		for i, c := range candidates {
			if c.Predicates != nil || c.Segment != r.Segment {
				continue
			}
			if c.Order == r.Order {
				// the replacement keeps the position registration gave it
				candidates[i] = r
				return candidates
			}
			candidates = slices.Delete(candidates, i, i+1)
			break
		}
	}
	// the route lands after every candidate of no greater Order, so equal
	// orders keep their registration order
	i := len(candidates)
	for i > 0 && candidates[i-1].Order > r.Order {
		i--
	}
	return slices.Insert(candidates, i, r)
}

func candidateSlot(like *route.Route, candidates ...*route.Route) *route.Route {
	// Segment is read from each candidate rather than the slot, since one
	// path may hold both kinds
	return &route.Route{
		ExactMatch: like.ExactMatch,
		Method:     like.Method,
		Host:       like.Host,
		Path:       like.Path,
		Implicit:   like.Implicit,
		Candidates: candidates,
	}
}

func (rt *lmRouter) sort() {
	// prefixes and regex patterns sort longest first, registration order
	// breaking ties, so evaluation order is deterministic
	for _, lookup := range []route.HostRouteSetLookup{rt.routes, rt.wildcards, rt.anyDepth} {
		for _, hrc := range lookup {
			sortHostRouteSet(hrc)
		}
	}
}

func sortHostRouteSet(hrc *route.HostRouteSet) {
	if len(hrc.PrefixMatchRoutes) > 0 {
		prs := prefixRouteSets(hrc.PrefixMatchRoutes)
		slices.SortFunc(prs, func(a, b *route.PrefixRouteSet) int {
			return cmp.Compare(b.PathLen, a.PathLen)
		})
		hrc.PrefixMatchRoutes = route.PrefixRouteSets(prs)
	}
	if len(hrc.RegexMatchRoutes) > 0 {
		slices.SortStableFunc(hrc.RegexMatchRoutes,
			func(a, b *route.RegexRouteSet) int {
				return cmp.Compare(b.PatternLen, a.PatternLen)
			})
	}
}

// Handler resolves the request by host tier, then path tier, then method, and
// answers with the first match; an unsatisfied conditioned route is passed over
func (rt *lmRouter) Handler(r *http.Request) http.Handler {
	// host tiers are the exact hostname, then at each label boundary outward
	// a *.suffix wildcard before a **.suffix one, then the global host
	var q reqmatching.QueryValues
	if rt.matchScheme&router.MatchHostname == router.MatchHostname {
		host := r.Host
		i := strings.Index(host, ":")
		if i >= 0 {
			host = host[0:i]
		}
		// hosts are registered normalized, so a fully qualified request
		// hostname is stripped of its trailing dot before any lookup
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if h := rt.matchByHost(rt.routes, r, host, &q); h != nil {
			return h
		}
		if len(rt.wildcards) > 0 || len(rt.anyDepth) > 0 {
			if i := strings.IndexByte(host, '.'); i > 0 {
				suffix := host[i+1:]
				if len(rt.wildcards) > 0 {
					if h := rt.matchByHost(rt.wildcards, r, suffix, &q); h != nil {
						return h
					}
				}
				if len(rt.anyDepth) > 0 {
					if h := rt.matchAnyDepth(r, suffix, &q); h != nil {
						return h
					}
				}
			}
		}
	}
	if h := rt.matchByHost(rt.routes, r, "", &q); h != nil {
		return h
	}
	return notFoundHandler
}

func (rt *lmRouter) matchAnyDepth(r *http.Request, suffix string, q *reqmatching.QueryValues) http.Handler {
	// one map lookup on a substring of the host per label boundary
	for {
		if h := rt.matchByHost(rt.anyDepth, r, suffix, q); h != nil {
			return h
		}
		j := strings.IndexByte(suffix, '.')
		if j <= 0 {
			return nil
		}
		suffix = suffix[j+1:]
	}
}

func (rt *lmRouter) matchByHost(lookup route.HostRouteSetLookup, r *http.Request,
	host string, q *reqmatching.QueryValues,
) http.Handler {
	// within a tier the path order is exact, longest prefix, then regex; a
	// slot whose candidates the request all miss falls to the next path
	hrc, ok := lookup[host]
	if !ok || hrc == nil {
		return nil
	}
	method, path := r.Method, r.URL.Path
	if rs, ok := hrc.ExactMatchRoutes[path]; ok && rs != nil {
		rt := routeForMethod(rs, method)
		if rt == nil {
			return methodNotAllowedHandler
		}
		if rt.Candidates == nil {
			return rt.Handler
		}
		if h := selectCandidate(rt.Candidates, r, q); h != nil {
			return h
		}
	}
	if rt.matchScheme&router.MatchPathPrefix == router.MatchPathPrefix {
		if hrc.HasSegments {
			if h := matchSegmentPrefixes(hrc.PrefixMatchRoutes, r, method, path, q); h != nil {
				return h
			}
		} else {
			lp := len(path)
			for _, prc := range hrc.PrefixMatchRoutes {
				if prc.PathLen > lp || !strings.HasPrefix(path, prc.Path) {
					continue
				}
				rt := routeForMethod(prc.RoutesByMethod, method)
				if rt == nil {
					return methodNotAllowedHandler
				}
				if rt.Candidates == nil {
					return rt.Handler
				}
				if h := selectCandidate(rt.Candidates, r, q); h != nil {
					return h
				}
			}
		}
	}
	// the regex tier is evaluated only after exact and prefix both miss;
	// iterating a host's empty regex set costs nothing
	if rt.matchScheme&router.MatchPathRegex == router.MatchPathRegex {
		for _, rrs := range hrc.RegexMatchRoutes {
			if !rrs.Regexp.MatchString(path) {
				continue
			}
			rt := routeForMethod(rrs.RoutesByMethod, method)
			if rt == nil {
				return methodNotAllowedHandler
			}
			if rt.Candidates == nil {
				return rt.Handler
			}
			if h := selectCandidate(rt.Candidates, r, q); h != nil {
				return h
			}
		}
	}
	return nil
}

func matchSegmentPrefixes(sets route.PrefixRouteSets, r *http.Request, method, path string,
	q *reqmatching.QueryValues,
) http.Handler {
	// the prefix loop for a host with segment routes: one matches only where
	// the prefix ends on an element boundary, /foo and /foo/bar but not /foobar
	lp := len(path)
	for _, prc := range sets {
		if prc.PathLen > lp || !strings.HasPrefix(path, prc.Path) {
			continue
		}
		boundary := prc.PathLen == lp || path[prc.PathLen] == '/' ||
			prc.Path[prc.PathLen-1] == '/'
		if !boundary && !prc.Plain {
			// every route here needs a boundary this path does not have
			continue
		}
		rt := routeForMethod(prc.RoutesByMethod, method)
		if rt == nil {
			return methodNotAllowedHandler
		}
		h, eligible := selectPrefixRoute(rt, boundary, r, q)
		if h != nil {
			return h
		}
		if !eligible {
			// the path matched through another method's plain route, and
			// this method has nothing that matches its shape
			return methodNotAllowedHandler
		}
	}
	return nil
}

func routeForMethod(routes route.Lookup, method string) *route.Route {
	if rt := routes[method]; rt != nil {
		return rt
	}
	return routes[meth.Wildcard]
}

func selectPrefixRoute(rt *route.Route, boundary bool, r *http.Request,
	q *reqmatching.QueryValues,
) (http.Handler, bool) {
	// a route needing an absent boundary is skipped; eligible reports whether
	// any route survived that filter
	if rt.Candidates == nil {
		if rt.Segment && !boundary {
			return nil, false
		}
		return rt.Handler, true
	}
	var eligible bool
	for _, c := range rt.Candidates {
		if c.Segment && !boundary {
			continue
		}
		eligible = true
		if c.Predicates == nil || c.Predicates.Match(r, q) {
			return c.Handler, true
		}
	}
	return nil, eligible
}

func selectCandidate(candidates []*route.Route, r *http.Request, q *reqmatching.QueryValues) http.Handler {
	for _, c := range candidates {
		if c.Predicates == nil || c.Predicates.Match(r, q) {
			return c.Handler
		}
	}
	return nil
}

func (rt *lmRouter) SetMatchingScheme(s router.MatchingScheme) {
	rt.matchScheme = s
}

func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
}

var (
	methodNotAllowedHandler = http.HandlerFunc(MethodNotAllowed)
	notFoundHandler         = http.NotFoundHandler()
)

// prefixRouteSets allows the route.PrefixRouteSets to be sorted by path from
// longest-to-shortest using sort.Interface
type prefixRouteSets route.PrefixRouteSets

func (prs prefixRouteSets) Len() int {
	return len(prs)
}

func (prs prefixRouteSets) Swap(i, j int) {
	prs[i], prs[j] = prs[j], prs[i]
}

func (prs prefixRouteSets) Less(i, j int) bool {
	return prs[i].PathLen > prs[j].PathLen
}
