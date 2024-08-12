package router

import (
	"net/http"
)

type Router interface {
	// ServeHTTP services the provided HTTP Request and write the Response
	ServeHTTP(http.ResponseWriter, *http.Request)
	// RegisterRoute registers a handler for the provided path/host/method(s)
	RegisterRoute(path string, hosts, methods []string, matchPrefix bool,
		handler http.Handler) error
	// Handler returns the handler matching the method/host/path in the Request
	Handler(*http.Request) http.Handler
	// SetMatchingScheme specifies the ways the Router matches requests
	SetMatchingScheme(MatchingScheme)
}

type MatchingScheme int

const (
	MatchHostname   MatchingScheme = 1
	MatchPathPrefix MatchingScheme = 2

	DefaultMatchingScheme MatchingScheme = MatchHostname | MatchPathPrefix
)

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
