/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	origins "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
)

// TLSCertConfig returns the crypto/tls configuration object with a list of name-bound certs derifed from the running config
func (c *Config) TLSCertConfig() (*tls.Config, error) {
	var err error
	if !c.Frontend.ServeTLS {
		return nil, nil
	}
	to := []*origins.Options{}
	for _, oc := range c.Origins {
		if oc.TLS.ServeTLS {
			to = append(to, oc)
		}
	}

	l := len(to)
	if l == 0 {
		return nil, nil
	}

	tlsConfig := &tls.Config{NextProtos: []string{"h2"}}
	tlsConfig.Certificates = make([]tls.Certificate, l)

	for i, tc := range to {
		tlsConfig.Certificates[i], err = tls.LoadX509KeyPair(tc.TLS.FullChainCertPath, tc.TLS.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
	}

	tlsConfig.BuildNameToCertificate()

	return tlsConfig, nil

}
