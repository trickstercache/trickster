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

package options

import (
	"fmt"
)

// InvalidDiscoveryOptionsError is an error type for invalid discoverer Options
type InvalidDiscoveryOptionsError struct {
	error
}

// NewErrInvalidDiscovererName returns an error for an unusable discoverer name
func NewErrInvalidDiscovererName(name string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid discoverer name %q`, name),
	}
}

// NewErrMissingDiscoveryProvider returns an error for a discoverer with no provider
func NewErrMissingDiscoveryProvider(name string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`missing provider for discoverer %q`, name),
	}
}

// NewErrInvalidDiscoveryProvider returns an error for an unsupported provider name
func NewErrInvalidDiscoveryProvider(provider, name string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid provider %q for discoverer %q`,
			provider, name),
	}
}

// NewErrInvalidDiscoveryBlock returns an error for a provider-specific options
// block that does not match the discoverer's provider
func NewErrInvalidDiscoveryBlock(block, provider, name string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(
			`the %q options block is not valid for provider %q in discoverer %q`,
			block, provider, name),
	}
}

// NewErrInvalidKubernetesOptions returns an error for invalid kubernetes
// connection options
func NewErrInvalidKubernetesOptions(name, detail string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid kubernetes options for discoverer %q: %s`,
			name, detail),
	}
}

// NewErrInvalidDNSOptions returns an error for invalid dns resolver options
func NewErrInvalidDNSOptions(name, detail string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid dns options for discoverer %q: %s`,
			name, detail),
	}
}

// InvalidQueryError is an error type for an invalid alb.discovery query
type InvalidQueryError struct {
	error
}

// NewErrInvalidQuery returns an error for an invalid discovery query
func NewErrInvalidQuery(albName, detail string) error {
	return &InvalidQueryError{
		error: fmt.Errorf(`invalid discovery query for alb %q: %s`,
			albName, detail),
	}
}

// NewErrInvalidQueryField returns an error for a query field that is not
// valid for the discoverer's provider
func NewErrInvalidQueryField(albName, fieldName, provider string) error {
	return &InvalidQueryError{
		error: fmt.Errorf(
			`invalid discovery query for alb %q: %q is not valid for the %s provider`,
			albName, fieldName, provider),
	}
}

// NewErrInvalidFileOptions returns an error for invalid file provider options
func NewErrInvalidFileOptions(name, detail string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid file options for discoverer %q: %s`,
			name, detail),
	}
}
