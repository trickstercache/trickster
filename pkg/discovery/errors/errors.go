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

// Package errors defines the error types shared by the discovery options
// packages.
//
// It exists to break an import cycle rather than for tidiness. Each
// provider's options package owns the error for its own block, so that
// adding a provider does not mean editing the base options package; but
// pkg/discovery/options imports every provider's options package, so a
// provider's options package cannot import it back to reach the shared
// type. This package is the leaf both sides can reach: it imports nothing
// from Trickster, and must stay that way.
package errors

import (
	"fmt"
)

// InvalidDiscoveryOptionsError is an error type for invalid discoverer
// Options. Every invalid-options error in the discovery tree carries this
// type, whichever package constructed it, so a caller can recognize a bad
// discovery config without knowing which provider produced it.
type InvalidDiscoveryOptionsError struct {
	error
}

// NewInvalidOptions returns an error for an invalid provider options block.
// The provider's own options package calls this with its provider name, so
// the message reads the same for every provider without the base options
// package holding a constructor per provider.
func NewInvalidOptions(provider, name, detail string) error {
	return &InvalidDiscoveryOptionsError{
		error: fmt.Errorf(`invalid %s options for discoverer %q: %s`,
			provider, name, detail),
	}
}

// Newf returns an InvalidDiscoveryOptionsError wrapping a formatted message
func Newf(format string, args ...any) error {
	return &InvalidDiscoveryOptionsError{error: fmt.Errorf(format, args...)}
}
