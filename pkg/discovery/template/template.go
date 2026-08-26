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

// Package template instantiates backend Options for discovered pool members
// by cloning a template backend (a backend configured with is_template: true)
// and overlaying the member's addressing.
//
// A discovered member overrides exactly: the backend name, origin_url, and
// its derived scheme/host/path-prefix. A member's weight applies to the
// ALB pool entry, not the backend, and its path prefix falls back to the
// template's when the provider did not supply one. Everything else — cache
// settings, TLS client config, healthcheck, paths, rewriters, timeouts,
// tracing, and all other options — is inherited from the template.
package template

import (
	"fmt"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
)

// Instantiate returns a live backend Options for the discovered member,
// cloned from the provided template. name is the generated backend name
// (see discovery.Snapshot.BackendNames) and must be unique among backends.
func Instantiate(name string, tmpl *bo.Options, m discovery.Member) (*bo.Options, error) {
	if tmpl == nil {
		return nil, fmt.Errorf("nil template backend options for member %q", name)
	}
	if !tmpl.IsTemplate {
		return nil, fmt.Errorf("backend %q is not a template (is_template: true) and cannot instantiate member %q",
			tmpl.Name, name)
	}
	if m.Address == "" {
		return nil, fmt.Errorf("discovered member %q has no address", name)
	}
	out := tmpl.Clone()
	out.IsTemplate = false
	out.IsDefault = false
	// the template's implicit replica group (defaulted to its own name by
	// Initialize) must not leak into members; clear it so Initialize
	// re-defaults it to the member name. An operator-set group (differing
	// from the template name) is inherited so TSM replica semantics hold,
	// and a provider-conveyed per-member group (e.g. from a configured
	// kubernetes label) overrides both.
	if out.ReplicaGroup == tmpl.Name {
		out.ReplicaGroup = ""
	}
	if m.ReplicaGroup != "" {
		out.ReplicaGroup = m.ReplicaGroup
	}
	mm := m.Clone()
	if mm.PathPrefix == "" {
		mm.PathPrefix = tmpl.PathPrefix
	}
	out.OriginURL = mm.URL()
	// CacheKeyPrefix, Scheme, Host and PathPrefix are derived from OriginURL
	// by Initialize; clear the template's derivations first so they cannot
	// leak through
	out.Scheme = ""
	out.Host = ""
	out.PathPrefix = ""
	if out.CacheKeyPrefix == tmpl.Host {
		out.CacheKeyPrefix = ""
	}
	if err := out.Initialize(name); err != nil {
		return nil, err
	}
	return out, nil
}
