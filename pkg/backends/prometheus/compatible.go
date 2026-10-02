/*
 * Copyright 2026 The Trickster Authors
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

package prometheus

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Hooks describes the differences of a Prometheus-compatible HTTP API.
// Zero values preserve the Prometheus provider's behavior.
type Hooks struct {
	PathPrefix      string
	CacheKeyParams  []string
	CacheKeyHeaders []string
	// PrepareRequest may normalize provider-specific inputs before cache lookup.
	// Returning false relays the original request without caching or rewriting.
	PrepareRequest    func(*http.Request) bool
	HealthCheckConfig func(*url.URL) *ho.Options
	// PreserveQueryGrid retains caller timestamps rather than rounding them.
	PreserveQueryGrid bool
	// AlignQueryGrid rounds range endpoints down to epoch-aligned steps while
	// retaining PreserveQueryGrid's millisecond parsing and wire precision.
	AlignQueryGrid bool
	// AllowedPaths, when set, lists the only catalogue paths served as usual; every other path
	// is answered locally by the unsupported handler rather than proxied
	AllowedPaths []string
	// MaxSeries is the series count at which the origin truncates a query result; 0 for none
	MaxSeries int
}

func pathPrefix(prefix string) string {
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		return "/" + prefix
	}
	return ""
}

// WithPathPrefix copies paths and prepends the API context root.
func WithPathPrefix(paths po.List, prefix string) po.List {
	out := paths.Clone()
	for _, p := range out {
		p.Path = pathPrefix(prefix) + p.Path
	}
	return out
}

// WithCacheKeyParams copies paths and adds query/form cache identity fields.
func WithCacheKeyParams(paths po.List, names ...string) po.List {
	out := paths.Clone()
	for _, p := range out {
		for _, name := range names {
			if !slices.Contains(p.CacheKeyParams, name) {
				p.CacheKeyParams = append(p.CacheKeyParams, name)
			}
		}
	}
	return out
}

// WithCacheKeyHeaders copies paths and adds case-insensitive header identity.
func WithCacheKeyHeaders(paths po.List, names ...string) po.List {
	out := paths.Clone()
	for _, p := range out {
		for _, name := range names {
			if !slices.ContainsFunc(p.CacheKeyHeaders, func(existing string) bool {
				return strings.EqualFold(existing, name)
			}) {
				p.CacheKeyHeaders = append(p.CacheKeyHeaders, http.CanonicalHeaderKey(name))
			}
		}
	}
	return out
}

// Restrict copies paths, rebinding each path not in allowed to the unsupported
// handler for every method, so that nothing outside allowed reaches the origin.
func Restrict(paths po.List, allowed []string) po.List {
	out := paths.Clone()
	if len(allowed) == 0 {
		return out
	}
	for _, p := range out {
		if !slices.Contains(allowed, p.Path) {
			p.HandlerName = handlerUnsupported
			p.Methods = methods.AllHTTPMethods()
		}
	}
	return out
}

// Without copies paths while omitting the specified handler names.
func Without(paths po.List, names ...string) po.List {
	out := make(po.List, 0, len(paths))
	for _, p := range paths {
		if !slices.Contains(names, p.HandlerName) {
			out = append(out, p.Clone())
		}
	}
	return out
}
