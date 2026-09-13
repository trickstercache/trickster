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

// package route provides a Route data structure for Request Routing
package route

import (
	"net/http"
	"regexp"

	pathmatching "github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
)

// Spec describes one route registration in full: where it matches, what it
// is conditioned on, and how it ranks among conditioned routes on one path
type Spec struct {
	Path      string
	Hosts     []string
	Methods   []string
	MatchType pathmatching.PathMatchType
	// Predicates conditions the route; nil registers an unconditioned route
	Predicates *matching.Predicates
	// Order ranks the route among those sharing a path and method: lower
	// values are tried first, and equal values in registration order
	Order   int
	Handler http.Handler
}

// Route is one registered handler. A method slot holding routes with
// predicates is a Route whose Candidates list them in registration order and
// whose own Handler is unused; a slot holding one unconditioned route is that
// route, with nil Candidates, so the unconditioned path costs one nil check.
type Route struct {
	ExactMatch bool
	Method     string
	Host       string
	Path       string
	Handler    http.Handler
	// Predicates conditions the route; nil for an unconditioned route
	Predicates *matching.Predicates
	// Order ranks the route among the candidates of its slot
	Order int
	// Segment requires a prefix route to match on a path element boundary,
	// so /foo matches /foo and /foo/bar but not /foobar
	Segment bool
	// Implicit marks the HEAD route a GET registration contributes; one that
	// names HEAD itself supersedes every implicit candidate of its slot
	Implicit bool
	// Candidates is the ordered list a conditioned slot resolves through
	Candidates []*Route
}

type Routes []*Route

type (
	Lookup       map[string]*Route
	LookupLookup map[string]Lookup
)

type PrefixRouteSet struct {
	Path    string
	PathLen int
	// Plain reports that some route here matches without a segment boundary,
	// so the set still matches a path that does not end on one
	Plain          bool
	RoutesByMethod Lookup
}

type (
	PrefixRouteSets      []*PrefixRouteSet
	PrefixRouteSetLookup map[string]*PrefixRouteSet
)

// RegexRouteSet represents a regex path route, evaluated only after exact and prefix
// matching both miss. Only routes declaring capture tokens pay to extract submatches.
type RegexRouteSet struct {
	Pattern        string
	PatternLen     int
	Regexp         *regexp.Regexp
	RoutesByMethod Lookup
}

type (
	RegexRouteSets      []*RegexRouteSet
	RegexRouteSetLookup map[string]*RegexRouteSet
)

type HostRouteSet struct {
	ExactMatchRoutes     LookupLookup
	PrefixMatchRoutes    PrefixRouteSets
	PrefixMatchRoutesLkp PrefixRouteSetLookup
	RegexMatchRoutes     RegexRouteSets
	RegexMatchRoutesLkp  RegexRouteSetLookup
	// HasSegments is set once a segment prefix is registered, selecting the
	// prefix loop that checks boundaries; hosts without one run the plain loop
	HasSegments bool
}

type HostRouteSetLookup map[string]*HostRouteSet
