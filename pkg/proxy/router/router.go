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

// package router provides an interface for routing HTTP requests to handlers
package router

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
)

type Router interface {
	// ServeHTTP services the provided HTTP Request and write the Response
	ServeHTTP(http.ResponseWriter, *http.Request)
	// RegisterRoute registers a handler for the provided path/host/method(s)
	// matchType indicates how the path is matched against incoming requests
	// (exact, prefix or regex); regex paths are evaluated only after exact and
	// prefix matching both miss
	// If hosts is nil, the route uses global-routing instead of host-based
	// If methods is nil, the route is applicable to GET and HEAD requests
	// If methods includes GET but not HEAD, HEAD is automatically included
	RegisterRoute(path string, hosts, methods []string,
		matchType matching.PathMatchType, handler http.Handler) error
	// Handler returns the handler matching the method/host/path in the Request
	Handler(*http.Request) http.Handler
	// SetMatchingScheme specifies the ways the Router matches requests
	SetMatchingScheme(MatchingScheme)
}

type MatchingScheme int

const (
	MatchExactPath  MatchingScheme = 0
	MatchHostname   MatchingScheme = 1
	MatchPathPrefix MatchingScheme = 2
	MatchPathRegex  MatchingScheme = 4

	DefaultMatchingScheme MatchingScheme = MatchHostname | MatchPathPrefix |
		MatchPathRegex
)
