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
	"sync"
)

// CertSwapper is used by a TLSConfig to dynamically update the running Listener's Certificate list
// This allows Trickster to load and unload TLS certificate configs without restarting the process
type CertSwapper struct {
	*sync.Mutex
	Certificates []tls.Certificate
}

var errNoCertificates = errors.New("tls: no certificates configured")

// NewSwapper returns a new *CertSwapper based on the provided certList
func NewSwapper(certList []tls.Certificate) *CertSwapper {
	return &CertSwapper{
		Mutex:        &sync.Mutex{},
		Certificates: certList,
	}
}

// GetCert returns the best-matching certificate for the provided clientHello
func (c *CertSwapper) GetCert(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.Lock()
	defer c.Unlock()

	if len(c.Certificates) == 0 {
		return nil, errNoCertificates
	}

	if len(c.Certificates) == 1 {
		// There's only one choice, so no point doing any work.
		return &c.Certificates[0], nil
	}

	for _, cert := range c.Certificates {
		if err := clientHello.SupportsCertificate(&cert); err == nil {
			return &cert, nil
		}
	}

	// If nothing matches, return the first certificate.
	return &c.Certificates[0], nil
}

// SetCerts safely updates the certs list for the subject *CertSwapper
func (c *CertSwapper) SetCerts(certs []tls.Certificate) {
	c.Lock()
	defer c.Unlock()
	c.Certificates = certs
}
