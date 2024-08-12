package router

import (
	"net/http"
)

type Router interface {
	// ServeHTTP services the provided HTTP Request and write the Response
	ServeHTTP(http.ResponseWriter, *http.Request)
	// RegisterRoute registers a handler for the provided path/host/method(s)
	// If hosts is nil, the route uses global-routing instead of host-based
	// If methods is nil, the route is applicable to GET and HEAD requests
	// If methods includes GET but not HEAD, HEAD is automatically included
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
