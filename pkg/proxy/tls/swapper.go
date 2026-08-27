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

package tls

import (
	"crypto/tls"
	"errors"
)

// CertSwapper is used by a TLSConfig to dynamically update the running
// Listener's Certificate list. This allows Trickster to load and unload TLS
// certificate configs without restarting the process
type CertSwapper interface {
	GetCert(*tls.ClientHelloInfo) (*tls.Certificate, error)
	SetCerts([]tls.Certificate)
}

// ErrNoCertificates is returned by GetCert() when no certs are configured
var ErrNoCertificates = errors.New("tls: no certificates configured")

// NewSwapper returns a new CertSwapper based on the provided certList.
// The returned value also implements CertStore.
func NewSwapper(certList []tls.Certificate) CertSwapper {
	sw := &certStore{}
	sw.SetCerts(certList)
	return sw
}
