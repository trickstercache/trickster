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

// Package challenge answers ACME http-01 (RFC 8555) and tls-alpn-01 (RFC 8737) challenges on
// Trickster's own listeners, on behalf of whichever Solver the ACME manager registers.
package challenge

import (
	"crypto/tls"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

const (
	// ProtocolACMETLS1 is the ALPN protocol a CA offers, alone, to validate with tls-alpn-01
	ProtocolACMETLS1 = "acme-tls/1"
	// HTTPPathPrefix is the path under which a CA requests http-01 key authorizations
	HTTPPathPrefix = "/.well-known/acme-challenge/"

	typeHTTP01    = "http-01"
	typeTLSALPN01 = "tls-alpn-01"
	resultServed  = "served"
	resultUnknown = "unknown"
	resultError   = "error"
)

// Solver produces challenge responses for the challenges the process's ACME manager has pending
type Solver interface {
	// ServeHTTPChallenge writes the key authorization for a pending http-01 challenge, reporting
	// whether it did; it writes nothing when no challenge matches the request
	ServeHTTPChallenge(w http.ResponseWriter, r *http.Request) bool
	// TLSALPNCertificate returns the validation certificate for a pending tls-alpn-01 challenge
	TLSALPNCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

type holder struct {
	s Solver
}

var current atomic.Pointer[holder]

// SetSolver registers the Solver that answers challenges; nil unregisters it
func SetSolver(s Solver) {
	if s == nil {
		current.Store(nil)
		return
	}
	current.Store(&holder{s: s})
}

// HTTPHandler answers http-01 challenges ahead of next; any other request under the challenge
// path gets an empty 404 and never reaches a backend
func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, HTTPPathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if h := current.Load(); h != nil && h.s.ServeHTTPChallenge(w, r) {
			metrics.ACMEChallengeRequestsTotal.WithLabelValues(typeHTTP01, resultServed).Inc()
			return
		}
		metrics.ACMEChallengeRequestsTotal.WithLabelValues(typeHTTP01, resultUnknown).Inc()
		w.WriteHeader(http.StatusNotFound)
	})
}

// ConfigForClient is a tls.Config GetConfigForClient hook. It returns nil for every ordinary
// handshake, and a challenge-only config for a CA validating with tls-alpn-01.
func ConfigForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if len(hello.SupportedProtos) != 1 || hello.SupportedProtos[0] != ProtocolACMETLS1 {
		return nil, nil
	}
	h := current.Load()
	if h == nil {
		metrics.ACMEChallengeRequestsTotal.WithLabelValues(typeTLSALPN01, resultUnknown).Inc()
		return nil, nil
	}
	cert, err := h.s.TLSALPNCertificate(hello)
	if err != nil || cert == nil {
		metrics.ACMEChallengeRequestsTotal.WithLabelValues(typeTLSALPN01, resultError).Inc()
		return nil, err
	}
	metrics.ACMEChallengeRequestsTotal.WithLabelValues(typeTLSALPN01, resultServed).Inc()
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{ProtocolACMETLS1},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
