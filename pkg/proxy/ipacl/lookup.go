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

package ipacl

import (
	"fmt"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
)

// Lookup maps an ACL name to its definition.
type Lookup map[string]*Options

// Clone returns a copy of the lookup. Each compiled list is shared, because
// a list is immutable.
func (l Lookup) Clone() Lookup {
	if l == nil {
		return nil
	}
	out := make(Lookup, len(l))
	for name, options := range l {
		out[name] = options.Clone()
	}
	return out
}

// Clone returns a copy of the definition. The compiled list is shared.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.Allow = slices.Clone(o.Allow)
	out.Deny = slices.Clone(o.Deny)
	out.Rules = slices.Clone(o.Rules)
	return &out
}

// Validate names each definition by its map key, refuses an empty name and the reserved none, and compiles the
// list; it returns the warnings for the loader
func (l Lookup) Validate() ([]string, error) {
	var warnings []string
	for name, options := range l {
		if options == nil || name == "" || reserved.IsReference(name) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
		}
		options.Name = name
		list, found, err := Compile(*options)
		if err != nil {
			return nil, fmt.Errorf("ip acl %q: %w", name, err)
		}
		options.Compiled = list
		warnings = append(warnings, found...)
	}
	return warnings, nil
}
