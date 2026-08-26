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
)

type Route struct {
	ExactMatch bool
	Method     string
	Host       string
	Path       string
	Handler    http.Handler
}

type Routes []*Route

type (
	Lookup       map[string]*Route
	LookupLookup map[string]Lookup
)

type PrefixRouteSet struct {
	Path           string
	PathLen        int
	RoutesByMethod Lookup
}

type (
	PrefixRouteSets      []*PrefixRouteSet
	PrefixRouteSetLookup map[string]*PrefixRouteSet
)

// RegexRouteSet represents a regex path route, evaluated only after exact and
// prefix matching both miss
//
// Capture-group design (not yet implemented): request rewriters (ingress-nginx
// rewrite-target migration, Gateway API URLRewrite) will need submatch values
// from the winning pattern. The router's hot path stays capture-free: the lm
// router matches with MatchString only, which never allocates. When a path's
// config declares it needs captures, registration wraps that route's handler
// in a middleware that re-runs FindStringSubmatch (plus SubexpNames for named
// groups) against the request path and stashes the results in the request
// context for pkg/proxy/request/rewriter consumption. Because the wrapper is
// attached per-route at registration time, no-capture routes and the router
// itself require no changes, and extraction cost is paid only by routes that
// opted in.
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
}

type HostRouteSetLookup map[string]*HostRouteSet
