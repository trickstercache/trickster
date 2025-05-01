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
	"net/http"
	"sort"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	meth "github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/router/route"
)

var _ router.Router = &lmRouter{}

type lmRouter struct {
	matchScheme router.MatchingScheme
	routes      route.HostRouteSetLookup
}

func NewRouter() router.Router {
	return &lmRouter{
		matchScheme: router.DefaultMatchingScheme,
		routes:      make(route.HostRouteSetLookup),
	}
}

var emptyHost = []string{""}
var defaultMethods = []string{http.MethodGet, http.MethodHead}

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
	matchPrefix bool, handler http.Handler) error {
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
	if hosts == nil {
		hosts = emptyHost
	}
	for _, h := range hosts {
		hrc, ok := rt.routes[h]
		if !ok || hrc == nil {
			hrc = &route.HostRouteSet{
				ExactMatchRoutes:     make(route.LookupLookup),
				PrefixMatchRoutes:    make(route.PrefixRouteSets, 0, 16),
				PrefixMatchRoutesLkp: make(route.PrefixRouteSetLookup),
			}
			rt.routes[h] = hrc
		}
		if !matchPrefix {
			rl, ok := hrc.ExactMatchRoutes[path]
			if rl == nil || !ok {
				rl = make(route.Lookup)
				hrc.ExactMatchRoutes[path] = rl
			}
			for _, m := range methods {
				rl[m] = &route.Route{
					ExactMatch: true,
					Method:     m,
					Host:       h,
					Path:       path,
					Handler:    handler,
				}
				if m == http.MethodGet {
					if _, ok := rl[http.MethodHead]; !ok {
						rl[http.MethodHead] = &route.Route{
							ExactMatch: true,
							Method:     http.MethodHead,
							Host:       h,
							Path:       path,
							Handler:    handler,
						}
					}
				}
			}
			continue
		}
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
		for _, m := range methods {
			prc.RoutesByMethod[m] = &route.Route{
				ExactMatch: true,
				Method:     m,
				Host:       h,
				Path:       path,
				Handler:    handler,
			}
			if m == http.MethodGet {
				if _, ok := prc.RoutesByMethod[http.MethodHead]; !ok {
					prc.RoutesByMethod[http.MethodHead] = &route.Route{
						ExactMatch: true,
						Method:     http.MethodHead,
						Host:       h,
						Path:       path,
						Handler:    handler,
					}
				}
			}
		}
	}
	rt.sort()
	return nil
}

// this sorts the prefix-match paths longest to shortest
func (rt *lmRouter) sort() {
	for _, hrc := range rt.routes {
		if len(hrc.PrefixMatchRoutes) == 0 {
			continue
		}
		prs := prefixRouteSets(hrc.PrefixMatchRoutes)
		sort.Sort(prs)
		hrc.PrefixMatchRoutes = route.PrefixRouteSets(prs)
	}
}

func (rt *lmRouter) Handler(r *http.Request) http.Handler {
	if rt.matchScheme&router.MatchHostname == router.MatchHostname {
		host := r.Host
		i := strings.Index(host, ":")
		if i >= 0 {
			host = host[0:i]
		}
		h := rt.matchByHost(r.Method, host, r.URL.Path)
		if h != nil {
			return h
		}
	}
	h := rt.matchByHost(r.Method, "", r.URL.Path)
	if h != nil {
		return h
	}
	return notFoundHandler
}

func (rt *lmRouter) matchByHost(method, host, path string) http.Handler {
	if hrc, ok := rt.routes[host]; ok && hrc != nil {
		if rs, ok := hrc.ExactMatchRoutes[path]; ok && rs != nil {
			r, ok := rs[method]
			if !ok || r == nil {
				return methodNotAllowedHandler
			}
			return r.Handler
		}
		if rt.matchScheme&router.MatchPathPrefix != router.MatchPathPrefix {
			return nil
		}
		lp := len(path)
		for _, prc := range hrc.PrefixMatchRoutes {
			if prc.PathLen > lp {
				continue
			}
			if strings.HasPrefix(path, prc.Path) {
				r, ok := prc.RoutesByMethod[method]
				if !ok || r == nil {
					return methodNotAllowedHandler
				}
				return r.Handler
			}
		}
	}
	return nil
}

func (rt *lmRouter) SetMatchingScheme(s router.MatchingScheme) {
	rt.matchScheme = s
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
}

var methodNotAllowedHandler = http.HandlerFunc(MethodNotAllowed)
var notFoundHandler = http.NotFoundHandler()

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
