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

// Package ondemand lets a listener obtain a certificate during the first handshake for a name its
// store does not cover, on behalf of whichever Provider the ACME manager registers.
package ondemand

import (
	"crypto/tls"
	"sync/atomic"
)

// Provider issues certificates during handshakes for names no stored certificate covers
type Provider interface {
	// Enabled reports whether the listener with the given group key issues on demand
	Enabled(listenerKey string) bool
	// Certificate returns a certificate for the handshake, or nil when issuance is declined
	Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// Matcher finds the stored certificate indexed for a server name
type Matcher interface {
	Match(serverName string) *tls.Certificate
}

type holder struct {
	p Provider
}

var current atomic.Pointer[holder]

// SetProvider registers the Provider that issues on demand; nil unregisters it
func SetProvider(p Provider) {
	if p == nil {
		current.Store(nil)
		return
	}
	current.Store(&holder{p: p})
}

// GetCertificate wraps getCert for a listener's group key; a handshake reaches the Provider
// only when one is registered, the listener issues on demand and the store has no match.
func GetCertificate(listenerKey string, store Matcher,
	getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error),
) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		h := current.Load()
		if h == nil || hello.ServerName == "" || !h.p.Enabled(listenerKey) {
			return getCert(hello)
		}
		if cert := store.Match(hello.ServerName); cert != nil {
			return cert, nil
		}
		if cert, err := h.p.Certificate(hello); err == nil && cert != nil {
			return cert, nil
		}
		return getCert(hello)
	}
}
