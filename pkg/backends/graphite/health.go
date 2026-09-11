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

package graphite

import (
	"net/http"
	"slices"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// DefaultHealthCheckConfig returns the default HealthCheck Config for this backend provider
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	o := ho.New()
	u := c.BaseUpstreamURL()
	o.Scheme = u.Scheme
	o.Host = u.Host
	// /metrics/find with a top-level query is cheap and, unlike /version, is
	// served by every Graphite-protocol implementation
	o.Path = u.Path + healthPath
	o.Query = healthQuery
	if auth, err := originAuthHeader(c.graphiteOptions()); err == nil && auth != "" {
		o.Headers = map[string]string{headers.NameAuthorization: auth}
	}
	return o
}

// FinalizeHealthCheckOptions returns the effective probe options, restoring the
// origin credential unless the operator overrode or opted out of Authorization.
func (c *Client) FinalizeHealthCheckOptions(o *ho.Options) *ho.Options {
	if o == nil {
		return nil
	}
	var auth string
	if a, err := originAuthHeader(c.graphiteOptions()); err == nil {
		auth = a
	}
	var keys []string
	for k := range o.Headers {
		if http.CanonicalHeaderKey(k) == headers.NameAuthorization {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		if auth != "" {
			if o.Headers == nil {
				o.Headers = map[string]string{}
			}
			o.Headers[headers.NameAuthorization] = auth
		}
		return o
	}
	// case-colliding keys collapse to one deterministic winner: the canonical
	// spelling if present, otherwise the lexicographically first key
	winner := slices.Min(keys)
	if slices.Contains(keys, headers.NameAuthorization) {
		winner = headers.NameAuthorization
	}
	v := o.Headers[winner]
	// an empty winner is the opt-out only against a configured credential;
	// otherwise it stays a present-but-empty header
	if len(keys) == 1 && (v != "" || auth == "") {
		return o
	}
	out := o.Clone()
	for _, k := range keys {
		delete(out.Headers, k)
	}
	if v != "" || auth == "" {
		out.Headers[headers.NameAuthorization] = v
	}
	return out
}
