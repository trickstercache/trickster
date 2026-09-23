/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package validate

import (
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

// nativeBackendProtocols includes protocols inherited from native ALB routes,
// whose terminal backends need no direct listener mapping of their own.
func nativeBackendProtocols(c *config.Config, registry native.Registry) map[string][]string {
	result := make(map[string][]string)
	for name, backend := range c.Backends {
		if backend == nil || backend.IsTemplate {
			continue
		}
		backend.NormalizeListenerNames()
		var protocols []string
		for _, listenerName := range backend.ListenerNames {
			if lo := c.Listeners[listenerName]; lo != nil && registry.Get(strings.ToLower(lo.Protocol)) != nil {
				protocols = append(protocols, strings.ToLower(lo.Protocol))
			}
		}
		result[name] = append(result[name], protocols...)
		if backend.Provider != providers.ALB || backend.ALBOptions == nil {
			continue
		}
		if ur := backend.ALBOptions.UserRouter; ur != nil {
			if ur.DefaultBackend != "" {
				result[ur.DefaultBackend] = append(result[ur.DefaultBackend], protocols...)
			}
			for _, mapping := range ur.Users {
				if mapping != nil && mapping.ToBackend != "" {
					result[mapping.ToBackend] = append(result[mapping.ToBackend], protocols...)
				}
			}
		} else {
			for _, member := range backend.ALBOptions.Pool {
				result[member.Name] = append(result[member.Name], protocols...)
			}
		}
	}
	for name, protocols := range result {
		slices.Sort(protocols)
		result[name] = slices.Compact(protocols)
	}
	return result
}
