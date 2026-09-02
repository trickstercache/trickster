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

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
)

// The errors below are the ones this package owns: the discoverer-level
// checks, and the shared 'http' block. An error for a provider's own
// options block belongs to that provider's options package -- see, for
// example, gcpopts.NewErrInvalidOptions -- so that adding a provider does
// not mean editing this file.

// NewErrInvalidDiscovererName returns an error for an unusable discoverer name
func NewErrInvalidDiscovererName(name string) error {
	return derrors.Newf(`invalid discoverer name %q`, name)
}

// NewErrMissingDiscoveryProvider returns an error for a discoverer with no provider
func NewErrMissingDiscoveryProvider(name string) error {
	return derrors.Newf(`missing provider for discoverer %q`, name)
}

// NewErrInvalidDiscoveryProvider returns an error for an unsupported provider name
func NewErrInvalidDiscoveryProvider(provider, name string) error {
	return derrors.Newf(`invalid provider %q for discoverer %q`, provider, name)
}

// NewErrInvalidDiscoveryBlock returns an error for a provider-specific options
// block that does not match the discoverer's provider
func NewErrInvalidDiscoveryBlock(block, provider, name string) error {
	return derrors.Newf(
		`the %q options block is not valid for provider %q in discoverer %q`,
		block, provider, name)
}

// NewErrInvalidHTTPOptions returns an error for invalid shared HTTP client
// options. The 'http' block is defined in this package and shared by every
// polling provider, so unlike the per-provider blocks it belongs here.
func NewErrInvalidHTTPOptions(name, detail string) error {
	return derrors.NewInvalidOptions("http", name, detail)
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
