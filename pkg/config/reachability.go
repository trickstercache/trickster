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

package config

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// BackendReachability follows dispatch edges from the selected listeners; no names selects all.
// HTTP returns every reachable backend; streams return only pool members and templates.
func (c *Config) BackendReachability(wantHTTP bool, listenerNames ...string) sets.Set[string] {
	reached := sets.NewStringSet()
	members := sets.NewStringSet()
	if c == nil {
		return reached
	}
	queue := make([]string, 0, len(c.Backends))
	add := func(name string) {
		if name != "" && c.Backends[name] != nil && !reached.Contains(name) {
			reached.Set(name)
			queue = append(queue, name)
		}
	}
	for name, backend := range c.Backends {
		if backend == nil || backend.IsTemplate {
			continue
		}
		for _, listenerName := range backend.ListenerNames {
			if len(listenerNames) > 0 && !slices.Contains(listenerNames, listenerName) {
				continue
			}
			lo := c.Listeners[listenerName]
			if lo != nil && (wantHTTP && (lo.Protocol == "" || lo.Protocol == listener.ProtocolHTTP) ||
				!wantHTTP && lo.IsStream()) {
				add(name)
				break
			}
		}
	}
	for len(queue) > 0 {
		backend := c.Backends[queue[0]]
		queue = queue[1:]
		if o := backend.ALBOptions; o != nil {
			for _, member := range o.Pool {
				members.Set(member.Name)
				add(member.Name)
			}
			if o.Discovery != nil {
				members.Set(o.Discovery.TemplateBackend)
				add(o.Discovery.TemplateBackend)
			}
			if wantHTTP && o.UserRouter != nil {
				add(o.UserRouter.DefaultBackend)
				for _, mapping := range o.UserRouter.Users {
					if mapping != nil {
						add(mapping.ToBackend)
					}
				}
			}
		}
		if !wantHTTP {
			continue
		}
		if o := c.Rules[backend.RuleName]; o != nil {
			add(o.NextRoute)
			for _, ruleCase := range o.CaseOptions {
				if ruleCase != nil {
					add(ruleCase.NextRoute)
				}
			}
		}
		for _, path := range backend.Paths {
			if path == nil {
				continue
			}
			for _, mirror := range path.Mirrors {
				if mirror != nil {
					add(mirror.BackendName)
				}
			}
		}
	}
	if wantHTTP {
		return reached
	}
	return members
}
