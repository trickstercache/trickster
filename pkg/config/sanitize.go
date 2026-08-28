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
	"slices"
	"strings"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	bp "github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	cache "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	tp "github.com/trickstercache/trickster/v2/pkg/observability/tracing/providers"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

const sanitizedSecret = "*****"
const sanitizedEndpoint = "example.com"

var unsanitizedPathHeaders = map[string]struct{}{
	"cache-control": {},
	"expires":       {},
}

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
	replicaGroupMap := anonymizedReplicaGroupNames(cp.Backends, backendNameMap)
	listenerNameMap := anonymizedListenerNames(cp.Listeners)
	authNameMap := anonymizedAuthenticatorNames(cp.Authenticators)
	tracingNameMap := anonymizedTracingNames(cp.TracingOptions)

	renamedCaches := make(cache.Lookup, len(cp.Caches))
	for oldName, opts := range cp.Caches {
		newName := cacheNameMap[oldName]
		if opts != nil {
			opts.Name = newName
			sanitizeRedisEndpoints(opts)
		}
		renamedCaches[newName] = opts
	}
	cp.Caches = renamedCaches

	renamedListeners := make(listenerconfig.Lookup, len(cp.Listeners))
	for oldName, opts := range cp.Listeners {
		renamedListeners[listenerNameMap[oldName]] = opts
	}
	cp.Listeners = renamedListeners

	renamedBackends := make(bo.Lookup, len(cp.Backends))
	for oldName, opts := range cp.Backends {
		newName := backendNameMap[oldName]
		if opts != nil {
			opts.Name = newName
			if newReplicaGroup, ok := replicaGroupMap[opts.ReplicaGroup]; ok {
				opts.ReplicaGroup = newReplicaGroup
			}
			if opts.OriginURL != "" {
				opts.OriginURL = sanitizedEndpoint
			}
			if opts.CacheKeyPrefix != "" {
				opts.CacheKeyPrefix = sanitizedEndpoint
			}
			if newCacheName, ok := cacheNameMap[opts.CacheName]; ok {
				opts.CacheName = newCacheName
			}
			if newAuthName, ok := authNameMap[opts.AuthenticatorName]; ok {
				opts.AuthenticatorName = newAuthName
			}
			if newTracingName, ok := tracingNameMap[opts.TracingConfigName]; ok {
				opts.TracingConfigName = newTracingName
			}
			if newListenerName, ok := listenerNameMap[opts.ListenerName]; ok {
				opts.ListenerName = newListenerName
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

	renamedTracing := make(tracing.Lookup, len(cp.TracingOptions))
	for oldName, opts := range cp.TracingOptions {
		newName := tracingNameMap[oldName]
		if opts != nil {
			opts.Name = newName
			if opts.Endpoint != "" {
				opts.Endpoint = sanitizedEndpoint
			}
		}
		renamedTracing[newName] = opts
	}
	cp.TracingOptions = renamedTracing

	sanitizeRequestRewriters(cp.RequestRewriters)

	for _, opts := range cp.Rules {
		sanitizeRuleReferences(opts, backendNameMap)
	}

	return cp
}

func anonymizedReplicaGroupNames(backends bo.Lookup,
	backendNames map[string]string,
) map[string]string {
	groups := make(map[string]struct{})
	for _, opts := range backends {
		if opts != nil && opts.ReplicaGroup != "" {
			groups[opts.ReplicaGroup] = struct{}{}
		}
	}
	names := sortedKeys(groups)
	out := make(map[string]string, len(names))
	custom := 0
	for _, name := range names {
		if backendName, ok := backendNames[name]; ok {
			out[name] = backendName
			continue
		}
		custom++
		out[name] = fmt.Sprintf("replica-group-%d", custom)
	}
	return out
}

func anonymizedListenerNames(listeners listenerconfig.Lookup) map[string]string {
	names := sortedKeys(listeners)
	out := make(map[string]string, len(listeners))
	custom := 0
	for _, name := range names {
		switch name {
		case listenerconfig.DefaultFrontendName, mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics:
			out[name] = name
		default:
			custom++
			out[name] = fmt.Sprintf("listener-%d", custom)
		}
	}
	return out
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

func anonymizedTracingNames(tracingOptions tracing.Lookup) map[string]string {
	names := sortedKeys(tracingOptions)
	counts := make(map[string]int)
	out := make(map[string]string, len(tracingOptions))
	for _, name := range names {
		provider := "tracing"
		if tracingOptions[name] != nil {
			provider = anonymizedTracingProviderName(tracingOptions[name].Provider)
		}
		counts[provider]++
		out[name] = fmt.Sprintf("%s-%d", provider, counts[provider])
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

func anonymizedTracingProviderName(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if id, ok := tp.Names[provider]; ok {
		return id.String()
	}
	if provider == "" {
		return "tracing"
	}
	return provider
}

func sanitizeRedisEndpoints(opts *cache.Options) {
	if opts == nil || opts.Redis == nil {
		return
	}
	if opts.Redis.Endpoint != "" {
		opts.Redis.Endpoint = sanitizedEndpoint
	}
	for i, endpoint := range opts.Redis.Endpoints {
		if endpoint != "" {
			opts.Redis.Endpoints[i] = sanitizedEndpoint
		}
	}
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

func sanitizeRequestRewriters(rewriters map[string]*rwopts.Options) {
	for _, opts := range rewriters {
		if opts == nil {
			continue
		}
		for _, instruction := range opts.Instructions {
			sanitizeRewriterInstruction(instruction)
		}
	}
}

func sanitizeRewriterInstruction(instruction []string) {
	if len(instruction) < 2 {
		return
	}
	switch strings.ToLower(instruction[0]) {
	case "host", "hostname":
		sanitizeRewriterValues(instruction, 2)
	case "header":
		if len(instruction) > 2 && strings.EqualFold(instruction[2], "host") {
			sanitizeRewriterValues(instruction, 3)
		}
	}
}

func sanitizeRewriterValues(instruction []string, start int) {
	for i := start; i < len(instruction); i++ {
		if instruction[i] != "" {
			instruction[i] = sanitizedEndpoint
		}
	}
}

func sanitizePathHeaderValues(opts *bo.Options) {
	for _, path := range opts.Paths {
		if path == nil {
			continue
		}
		for k := range path.RequestHeaders {
			if shouldSanitizePathHeader(k) {
				path.RequestHeaders[k] = sanitizedSecret
			}
		}
		for k := range path.ResponseHeaders {
			if shouldSanitizePathHeader(k) {
				path.ResponseHeaders[k] = sanitizedSecret
			}
		}
	}
}

func shouldSanitizePathHeader(name string) bool {
	_, ok := unsanitizedPathHeaders[strings.ToLower(name)]
	return !ok
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
