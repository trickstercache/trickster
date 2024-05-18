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

package config

import (
	"crypto/tls"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

// TLSCertConfig returns the crypto/tls configuration object with a list of name-bound
// certs derived from the running config
func (c *Config) TLSCertConfig() (*tls.Config, error) {
	var err error
	if !c.Frontend.ServeTLS {
		return nil, nil
	}
	to := []*bo.Options{}
	for _, o := range c.Backends {
		if o.TLS.ServeTLS {
			to = append(to, o)
		}
	}

	l := len(to)
	if l == 0 {
		return nil, nil
	}

	tlsConfig := &tls.Config{Certificates: make([]tls.Certificate, l), NextProtos: []string{"h2"}}

	for i, tc := range to {
		tlsConfig.Certificates[i], err = tls.LoadX509KeyPair(tc.TLS.FullChainCertPath, tc.TLS.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
	}

	return tlsConfig, nil

}
