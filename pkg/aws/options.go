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
package aws

import (
	"errors"
	"strings"
)

// DefaultService is the signing service applied when none is configured.
// It is Amazon Managed Service for Prometheus, preserving the behavior of
// every config written against the previous implementation, which could
// sign for nothing else.
const DefaultService = "aps"

var (
	// ErrIncompleteStaticCredentials is returned when only one half of a
	// static credential pair is configured
	ErrIncompleteStaticCredentials = errors.New(
		"aws: access_key and secret_key must be provided together")
	// ErrNoRegion is returned when no region could be resolved from the
	// configuration, the environment, the shared config file, or the
	// instance metadata service
	ErrNoRegion = errors.New(
		"aws: no region configured and none could be resolved from the environment, " +
			"shared config, or instance metadata")
	// ErrEmptyService is returned when the signing service is explicitly
	// set to whitespace
	ErrEmptyService = errors.New("aws: service cannot be empty")
	// ErrNilOptions is returned when a signer is constructed without options
	ErrNilOptions = errors.New("aws: nil options")
)

// Secret is a credential string that redacts itself when marshaled, so that
// a config dump or the management API never emits it.
type Secret string

// secretToken is what a Secret marshals to in place of its value.
const secretToken = "<secret>"

// MarshalYAML implements yaml.Marshaler.
func (s Secret) MarshalYAML() (any, error) {
	if s == "" {
		return nil, nil
	}
	return secretToken, nil
}

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return []byte(`"` + secretToken + `"`), nil
}

// String redacts the secret, so that accidental interpolation into a log or
// error message cannot leak it.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return secretToken
}

// Options configures credential resolution and SigV4 signing.
//
// Leaving the credential fields empty selects the standard AWS credential
// chain: environment variables, the shared config file, EKS IRSA and Pod
// Identity web-identity tokens, SSO, and the EC2 instance metadata service.
// That chain is the one thing here worth taking from the AWS SDK rather
// than hand-writing, and it is why the SDK's config and credentials modules
// are dependencies while its generated service clients deliberately are not.
type Options struct {
	// Region is the AWS region to sign for. When empty it is resolved from
	// the environment, the shared config file, or instance metadata.
	Region string `yaml:"region,omitempty"`
	// AccessKey and SecretKey are a static credential pair. Both or
	// neither; providing one alone is a configuration error rather than a
	// partial credential.
	AccessKey string `yaml:"access_key,omitempty"`
	SecretKey Secret `yaml:"secret_key,omitempty"`
	// Profile names a profile in the shared config file
	Profile string `yaml:"profile,omitempty"`
	// RoleARN is a role to assume with whatever credentials the chain
	// resolves first
	RoleARN string `yaml:"role_arn,omitempty"`
	// Service is the signing service name. It defaults to DefaultService
	// ("aps", Amazon Managed Service for Prometheus); set it to "ec2",
	// "ecs", "es" or similar to sign for another service.
	Service string `yaml:"service,omitempty"`
}

// GetService returns the configured signing service, or the default. It
// tolerates a nil receiver.
func (o *Options) GetService() string {
	if o == nil || strings.TrimSpace(o.Service) == "" {
		return DefaultService
	}
	return strings.TrimSpace(o.Service)
}

// Clone returns a deep copy.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Validate checks what can be checked without reaching the network.
//
// Region is deliberately not required here. Requiring it would break the
// standard AWS_REGION and instance-metadata paths, which are how most
// deployments supply it; an unresolvable region surfaces as ErrNoRegion
// when the signer first resolves, with a message naming every source it
// tried.
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if (o.AccessKey == "") != (o.SecretKey == "") {
		return ErrIncompleteStaticCredentials
	}
	if o.Service != "" && strings.TrimSpace(o.Service) == "" {
		return ErrEmptyService
	}
	return nil
}
