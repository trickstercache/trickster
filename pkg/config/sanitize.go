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

package config

import (
	"fmt"
	"sort"
	"strings"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	bp "github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	cache "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
)

// SanitizedString returns the running Config as YAML with private backend and
// cache names, origin URLs, and path header values anonymized.
func (c *Config) SanitizedString() string {
	return c.SanitizedClone().String()
}

// SanitizedClone returns a copy of the Config with backend and cache names
// anonymized while preserving all internal name references.
func (c *Config) SanitizedClone() *Config {
	cp := c.Clone()

	cacheNameMap := anonymizedCacheNames(cp.Caches)
	backendNameMap := anonymizedBackendNames(cp.Backends)
	authNameMap := anonymizedAuthenticatorNames(cp.Authenticators)

	renamedCaches := make(cache.Lookup, len(cp.Caches))
	for oldName, opts := range cp.Caches {
		newName := cacheNameMap[oldName]
		if opts != nil {
			opts.Name = newName
		}
		renamedCaches[newName] = opts
	}
	cp.Caches = renamedCaches

	renamedBackends := make(bo.Lookup, len(cp.Backends))
	for oldName, opts := range cp.Backends {
		newName := backendNameMap[oldName]
		if opts != nil {
			opts.Name = newName
			if opts.OriginURL != "" {
				opts.OriginURL = "example.com"
			}
			if opts.CacheKeyPrefix != "" {
				opts.CacheKeyPrefix = "example.com"
			}
			if newCacheName, ok := cacheNameMap[opts.CacheName]; ok {
				opts.CacheName = newCacheName
			}
			if newAuthName, ok := authNameMap[opts.AuthenticatorName]; ok {
				opts.AuthenticatorName = newAuthName
			}
			sanitizePathAuthenticatorReferences(opts, authNameMap)
			sanitizeBackendReferences(opts, backendNameMap)
			sanitizePathHeaderValues(opts)
		}
		renamedBackends[newName] = opts
	}
	cp.Backends = renamedBackends

	renamedAuthenticators := make(auth.Lookup, len(cp.Authenticators))
	for oldName, opts := range cp.Authenticators {
		newName := authNameMap[oldName]
		if opts != nil {
			opts.Name = newName
			sanitizeAuthenticatorUsers(opts)
		}
		renamedAuthenticators[newName] = opts
	}
	cp.Authenticators = renamedAuthenticators

	for _, opts := range cp.Rules {
		sanitizeRuleReferences(opts, backendNameMap)
	}

	return cp
}

func anonymizedCacheNames(caches cache.Lookup) map[string]string {
	names := sortedKeys(caches)
	counts := make(map[string]int)
	out := make(map[string]string, len(caches))
	for _, name := range names {
		provider := "cache"
		if caches[name] != nil {
			provider = anonymizedCacheProviderName(caches[name].Provider)
		}
		counts[provider]++
		out[name] = fmt.Sprintf("%s-%d", provider, counts[provider])
	}
	return out
}

func anonymizedBackendNames(backends bo.Lookup) map[string]string {
	names := sortedKeys(backends)
	counts := make(map[string]int)
	out := make(map[string]string, len(backends))
	for _, name := range names {
		provider := "backend"
		if backends[name] != nil {
			provider = anonymizedBackendProviderName(backends[name].Provider)
		}
		counts[provider]++
		out[name] = fmt.Sprintf("%s-%d", provider, counts[provider])
	}
	return out
}

func anonymizedAuthenticatorNames(authenticators auth.Lookup) map[string]string {
	names := sortedKeys(authenticators)
	out := make(map[string]string, len(authenticators))
	for i, name := range names {
		out[name] = fmt.Sprintf("auth%d", i+1)
	}
	return out
}

func anonymizedBackendProviderName(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == bp.Prometheus {
		return "prom"
	}
	if id, ok := bp.Names[provider]; ok {
		return id.String()
	}
	if provider == "" {
		return "backend"
	}
	return provider
}

func anonymizedCacheProviderName(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if id, ok := cp.Names[provider]; ok {
		return id.String()
	}
	if provider == "" {
		return "cache"
	}
	return provider
}

func sanitizePathAuthenticatorReferences(opts *bo.Options, authNameMap map[string]string) {
	for _, path := range opts.Paths {
		if path == nil {
			continue
		}
		if newName, ok := authNameMap[path.AuthenticatorName]; ok {
			path.AuthenticatorName = newName
		}
	}
}

func sanitizeBackendReferences(opts *bo.Options, backendNameMap map[string]string) {
	if opts.ALBOptions == nil {
		return
	}
	for i, name := range opts.ALBOptions.Pool {
		if newName, ok := backendNameMap[name]; ok {
			opts.ALBOptions.Pool[i] = newName
		}
	}
	if opts.ALBOptions.UserRouter == nil {
		return
	}
	if newName, ok := backendNameMap[opts.ALBOptions.UserRouter.DefaultBackend]; ok {
		opts.ALBOptions.UserRouter.DefaultBackend = newName
	}
	for _, user := range opts.ALBOptions.UserRouter.Users {
		if user == nil {
			continue
		}
		if newName, ok := backendNameMap[user.ToBackend]; ok {
			user.ToBackend = newName
		}
	}
}

func sanitizeRuleReferences(opts *rule.Options, backendNameMap map[string]string) {
	if opts == nil {
		return
	}
	if newName, ok := backendNameMap[opts.NextRoute]; ok {
		opts.NextRoute = newName
	}
	for _, c := range opts.CaseOptions {
		if c == nil {
			continue
		}
		if newName, ok := backendNameMap[c.NextRoute]; ok {
			c.NextRoute = newName
		}
	}
}

func sanitizeAuthenticatorUsers(opts *auth.Options) {
	userNames := sortedKeys(opts.Users)
	users := make(map[string]string, len(opts.Users))
	for i := range userNames {
		users[fmt.Sprintf("user%d", i+1)] = "redacted"
	}
	opts.Users = users
}

func sanitizePathHeaderValues(opts *bo.Options) {
	for _, path := range opts.Paths {
		if path == nil {
			continue
		}
		for k := range path.RequestHeaders {
			path.RequestHeaders[k] = "*****"
		}
		for k := range path.ResponseHeaders {
			path.ResponseHeaders[k] = "*****"
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
