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

import "net/http"

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

type HostRouteSet struct {
	ExactMatchRoutes     LookupLookup
	PrefixMatchRoutes    PrefixRouteSets
	PrefixMatchRoutesLkp PrefixRouteSetLookup
}

type HostRouteSetLookup map[string]*HostRouteSet
