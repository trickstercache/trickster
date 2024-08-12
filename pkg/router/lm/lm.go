// package lm represents a simple Longest Match router
package lm

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/router"
)

var _ router.Router = &rtr{}

type rtr struct {
	matchScheme router.MatchingScheme
	routes      router.HostRouteSetLookup
}

func NewRouter() router.Router {
	return &rtr{
		matchScheme: router.DefaultMatchingScheme,
		routes:      make(router.HostRouteSetLookup),
	}
}

var emptyHost = []string{""}

func (rt *rtr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.RequestURI == "*" {
		if r.ProtoAtLeast(1, 1) {
			w.Header().Set(headers.NameConnection, headers.ValueClose)
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rt.Handler(r).ServeHTTP(w, r)
}

func (rt *rtr) RegisterRoute(path string, hosts, methods []string,
	isWildcard bool, handler http.Handler) error {
	pl := len(path)
	if pl == 0 {
		return errors.ErrInvalidPath
	}
	if len(methods) == 0 {
		return errors.ErrInvalidMethod
	}
	if hosts == nil {
		hosts = emptyHost
	}
	for _, h := range hosts {
		hrc, ok := rt.routes[h]
		if !ok || hrc == nil {
			hrc = &router.HostRouteSet{
				ExactMatchRoutes:     make(router.RouteLookupLookup),
				PrefixMatchRoutes:    make(router.PrefixRouteSets, 0, 16),
				PrefixMatchRoutesLkp: make(router.PrefixRouteSetLookup),
			}
			rt.routes[h] = hrc
		}
		if !isWildcard {
			rl, ok := hrc.ExactMatchRoutes[path]
			if rl == nil || !ok {
				rl = make(router.RouteLookup)
				hrc.ExactMatchRoutes[path] = rl
			}
			for _, m := range methods {
				rl[m] = &router.Route{
					ExactMatch: true,
					Method:     m,
					Host:       h,
					Path:       path,
					Handler:    handler,
				}
				if m == http.MethodGet {
					if _, ok := rl[http.MethodHead]; !ok {
						rl[http.MethodHead] = &router.Route{
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
			prc = &router.PrefixRouteSet{
				Path:           path,
				PathLen:        pl,
				RoutesByMethod: make(router.RouteLookup),
			}
			hrc.PrefixMatchRoutesLkp[path] = prc
			if len(hrc.PrefixMatchRoutes) == 0 {
				hrc.PrefixMatchRoutes = make(router.PrefixRouteSets, 0, 16)
			}
			hrc.PrefixMatchRoutes = append(hrc.PrefixMatchRoutes, prc)
		}
		for _, m := range methods {
			prc.RoutesByMethod[m] = &router.Route{
				ExactMatch: true,
				Method:     m,
				Host:       h,
				Path:       path,
				Handler:    handler,
			}
			if m == http.MethodGet {
				if _, ok := prc.RoutesByMethod[http.MethodHead]; !ok {
					prc.RoutesByMethod[http.MethodHead] = &router.Route{
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

// this sorts the prefix-match paths longest to shorted
func (rt *rtr) sort() {
	for _, hrc := range rt.routes {
		if len(hrc.PrefixMatchRoutes) == 0 {
			continue
		}
		prs := prefixRouteSets(hrc.PrefixMatchRoutes)
		sort.Sort(prs)
		slices.Reverse(prs)
		hrc.PrefixMatchRoutes = router.PrefixRouteSets(prs)
	}
}

func (rt *rtr) Handler(r *http.Request) http.Handler {
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

func (rt *rtr) matchByHost(method, host, path string) http.Handler {
	if hrc, ok := rt.routes[host]; ok && hrc != nil {
		if rs, ok := hrc.ExactMatchRoutes[path]; ok && rs != nil {
			r, ok := rs[method]
			if !ok || r == nil {
				return methodNotAllowedHandler
			}
			return r.Handler
		}
		if !(rt.matchScheme&router.MatchPathPrefix == router.MatchPathPrefix) {
			return nil
		}
		lp := len(path)
		for _, prc := range hrc.PrefixMatchRoutes {
			if prc.PathLen > lp {
				continue
			}
			if strings.HasPrefix(prc.Path, path) {
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

func (rt *rtr) SetMatchingScheme(s router.MatchingScheme) {
	rt.matchScheme = s
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
}

var methodNotAllowedHandler = http.HandlerFunc(MethodNotAllowed)
var notFoundHandler = http.HandlerFunc(http.NotFound)

type prefixRouteSets router.PrefixRouteSets

func (prs prefixRouteSets) Len() int {
	return len(prs)
}

func (prs prefixRouteSets) Swap(i, j int) {
	prs[i], prs[j] = prs[j], prs[i]
}

func (prs prefixRouteSets) Less(i, j int) bool {
	return prs[i].PathLen < prs[j].PathLen
}
