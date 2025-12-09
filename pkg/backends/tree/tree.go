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

// Package tree provides data structures that define the possible paths
// through chained Backends to assist with config Validation
package tree

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

type Entry struct {
	Name           string
	Type           string
	Pool           []string
	UserRouterPool []string
	TargetProvider string
}

type (
	Entries []*Entry
	Lookup  map[string]*Entry
)

// InvalidBackendRoutingError is an error type for Invalid Backend Routing
type InvalidBackendRoutingError struct {
	error
}

// NewErrInvalidBackendRouting returns an invalid User Router Options error
func NewErrInvalidBackendRouting(errorString string, a ...any) error {
	return &InvalidBackendRoutingError{
		error: fmt.Errorf(errorString, a...),
	}
}

func (e Entries) Validate() error {
	nobs := providers.NonOriginBackends()
	entryLookup := make(Lookup, len(e))
	for _, entry := range e {
		entryLookup[entry.Name] = entry
	}

	// validatePoolLoop validates a pool for self-inclusion and endless loops
	validatePoolLoop := func(name string, path sets.Set[string], pool []string, poolType string, follow func(string, sets.Set[string]) error) error {
		for _, p := range pool {
			if p == name {
				return NewErrInvalidBackendRouting("entry '%s' cannot include itself in its %s", name, poolType)
			}
			if _, seen := path[p]; seen {
				return NewErrInvalidBackendRouting("endless loop detected between '%s' and '%s' (%s)", name, p, poolType)
			}
			// Recursively walk the pool
			path2 := path.Clone()
			path2.Set(name)
			if err := follow(p, path2); err != nil {
				return err
			}
		}
		return nil
	}

	var follow func(name string, path sets.Set[string]) error
	follow = func(name string, path sets.Set[string]) error {
		ent, ok := entryLookup[name]
		if !ok {
			return NewErrInvalidBackendRouting("unknown entry '%s' referenced in UserRouterPool", name)
		}
		if len(ent.UserRouterPool) > 0 {
			return validatePoolLoop(name, path, ent.UserRouterPool, "UserRouterPool", follow)
		}
		return validatePoolLoop(name, path, ent.Pool, "Pool", follow)
	}

	var collectNonVirtualTypes func(name string, visited sets.Set[string], types sets.Set[string])
	collectNonVirtualTypes = func(name string, visited sets.Set[string], types sets.Set[string]) {
		if _, seen := visited[name]; seen {
			return
		}
		visited.Set(name)
		ent, ok := entryLookup[name]
		if !ok {
			return
		}
		if ent.Type != "" && !nobs.Contains(ent.Type) {
			types.Set(ent.Type)
		}
		// Recurse through both Pool and UserRouterPool
		for _, p := range ent.Pool {
			collectNonVirtualTypes(p, visited, types)
		}
		for _, p := range ent.UserRouterPool {
			collectNonVirtualTypes(p, visited, types)
		}
	}

	// Main validation loop
	for _, ent := range e {
		for _, p := range ent.Pool {
			if _, ok := entryLookup[p]; !ok {
				return NewErrInvalidBackendRouting("entry '%s' has an invalid pool member backend name: '%s'", ent.Name, p)
			}
			if p == ent.Name {
				return NewErrInvalidBackendRouting("entry '%s' cannot use itself as a pool member", ent.Name)
			}
		}
		if len(ent.UserRouterPool) > 0 {
			types := make(sets.Set[string])
			visited := make(sets.Set[string])
			for _, p := range ent.UserRouterPool {
				if _, ok := entryLookup[p]; !ok {
					return NewErrInvalidBackendRouting("entry '%s' has an invalid destination backend name: '%s'", ent.Name, p)
				}
				if p == ent.Name {
					return NewErrInvalidBackendRouting("entry '%s' cannot use itself as a destination", ent.Name)
				}
				collectNonVirtualTypes(p, visited, types)
			}
			if len(types) > 1 {
				typeList := make([]string, 0, len(types))
				for t := range types {
					typeList = append(typeList, t)
				}
				return NewErrInvalidBackendRouting("entry '%s' subtree includes multiple non-virtual types: %v", ent.Name, typeList)
			} else if len(types) == 1 {
				ent.TargetProvider = types.Keys()[0]
			}
		}
		path := make(sets.Set[string])
		if err := follow(ent.Name, path); err != nil {
			return err
		}
	}
	return nil
}
