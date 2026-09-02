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

// Package aws provides AWS credential resolution and SigV4 request signing,
// shared by the proxy's outbound requests and by the autodiscovery
// providers that read AWS APIs.
//
// # Why this exists
//
// Trickster previously signed requests with github.com/prometheus/common/sigv4,
// which hardcodes the signing service as "aps" -- so the backend `sigv4`
// option worked for exactly one thing, Amazon Managed Service for Prometheus
// as an origin, despite its name promising general SigV4. That module was
// also the only path by which aws-sdk-go v1 entered the dependency graph,
// and v1 reached end of support on 2025-07-31.
//
// This package replaces it with aws-sdk-go-v2, adds a configurable signing
// service (still defaulting to aps, so existing configs are unchanged), and
// gives the discovery providers and the proxy one credential story instead
// of two.
//
// # Layering
//
// This package deliberately imports nothing from Trickster. pkg/backends/options
// depends on it for the backend `sigv4` block, and pkg/backends/options is
// itself reachable from pkg/observability/logging via pkg/config -- so any
// Trickster import here risks closing an import cycle. Errors are returned
// rather than logged for the same reason.
// Package secret defines a credential string that redacts itself.
package secret

// Secret is a credential string that redacts itself when marshaled, so that
// a config dump or the management API never emits it.
//
// It is a leaf type on purpose: every provider that carries a credential
// needs it, and none of them should have to import another provider's
// package to get it.
type Secret string

// Token is what a Secret marshals to in place of its value.
const Token = "<secret>"

// MarshalYAML implements yaml.Marshaler.
func (s Secret) MarshalYAML() (any, error) {
	if s == "" {
		return nil, nil
	}
	return Token, nil
}

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return []byte(`"` + Token + `"`), nil
}

// String redacts the secret, so that accidental interpolation into a log or
// error message cannot leak it.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return Token
}
