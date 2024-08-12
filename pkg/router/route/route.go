package route

import "net/http"

type Route struct {
	ExactMatch bool
	Method     string
	Host       string
	Path       string
	Handler    http.Handler
}

type Routes []*Route

type RouteLookup map[string]*Route
type RouteLookupLookup map[string]RouteLookup

type PrefixRouteSet struct {
	Path           string
	PathLen        int
	RoutesByMethod RouteLookup
}

type PrefixRouteSets []*PrefixRouteSet
type PrefixRouteSetLookup map[string]*PrefixRouteSet

type HostRouteSet struct {
	ExactMatchRoutes     RouteLookupLookup
	PrefixMatchRoutes    PrefixRouteSets
	PrefixMatchRoutesLkp PrefixRouteSetLookup
}

type HostRouteSetLookup map[string]*HostRouteSet
