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

package types

import (
	"net/http"
)

// Authenticator represents a specific auth implementation (e.g., Basic Auth)
type Authenticator interface {
	// Authenticate performs authentication against the provided request
	Authenticate(*http.Request) (*AuthResult, error)
	// ExtractCredentials returns the username, credentials and format (as
	// applicable) from the request.
	ExtractCredentials(*http.Request) (string, string, CredentialsFormat, error)
	// SetExtractCredentialsFunc allows the Authenticator to use a custom (e.g,
	// Backend-provider-specific) Credentials Extractor in lieu of the
	// Authenticator implementation's built-in Extractor.
	SetExtractCredentialsFunc(ExtractCredsFunc)
	// LoadUsers loads the provided users into the Authenticator. If the bool is
	// true, the existing list will be replaced, otherwise appended.
	LoadUsers(string, CredentialsFileFormat, CredentialsFormat, bool) error
	// AddUser adds the provided user to the Authenticator's users list
	AddUser(string, string, CredentialsFormat) error
	// RemoveUser removes the provided user from the Authenticator's users list
	RemoveUser(string)
	// Clone returns a new / independent duplicate of the Authenticator
	Clone() Authenticator
	// ProxyPreserve is true when the Authenticator will not strip Auth headers
	ProxyPreserve() bool
	// Sanitize must strip Auth headers from r only when ProxyPreserve is true
	Sanitize(*http.Request)
}

type Lookup map[string]Authenticator

// Provider is a defined type for the Authenticator Provider's name
type Provider string

type ExtractCredsFunc func(*http.Request) (string, string, CredentialsFormat, error)

// NewAuthenticatorFunc defines a function that returns a new Authenticator
type NewAuthenticatorFunc func(map[string]any) (Authenticator, error)

// RegistryEntry defines an entry in the ALB Registry
type RegistryEntry struct {
	Provider Provider
	New      NewAuthenticatorFunc
}

type IsRegisteredFunc func(Provider) bool

type CredentialsManifest map[string]string
