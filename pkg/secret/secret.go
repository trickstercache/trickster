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

// Package secret defines a credential string that redacts itself, and the keys that tokens are
// signed and sealed with.
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
