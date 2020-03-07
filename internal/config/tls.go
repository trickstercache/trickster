/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"crypto/tls"
	"io/ioutil"
)

// TLSConfig is a collection of TLS-related client and server configurations
type TLSConfig struct {
	// FullChainCertPath specifies the path of the file containing the
	// concatenated server certification and the intermediate certification for the tls endpoint
	FullChainCertPath string `toml:"full_chain_cert_path"`
	// PrivateKeyPath specifies the path of the private key file for the tls endpoint
	PrivateKeyPath string `toml:"private_key_path"`
	// ServeTLS is set to true once the Cert and Key files have been validated,
	// indicating the consumer of this config can service requests over TLS
	ServeTLS bool `toml:"-"`
	// InsecureSkipVerify indicates that the HTTPS Client in Trickster should bypass
	// hostname verification for the origin's certificate when proxying requests
	InsecureSkipVerify bool `toml:"insecure_skip_verify"`
	// CertificateAuthorities provides a list of custom Certificate Authorities for the upstream origin
	// which are considered in addition to any system CA's by the Trickster HTTPS Client
	CertificateAuthorityPaths []string `toml:"certificate_authority_paths"`
	// ClientCertPath provides the path to the Client Certificate when using Mutual Authorization
	ClientCertPath string `toml:"client_cert_path"`
	// ClientKeyPath provides the path to the Client Key when using Mutual Authorization
	ClientKeyPath string `toml:"client_key_path"`
}

// DefaultTLSConfig will return a *TLSConfig with the default settings
func DefaultTLSConfig() *TLSConfig {
	return &TLSConfig{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

// Clone returns an exact copy of the subject *TLSConfig
func (tc *TLSConfig) Clone() *TLSConfig {

	var caps []string
	if tc.CertificateAuthorityPaths != nil {
		caps = make([]string, len(tc.CertificateAuthorityPaths))
		copy(caps, tc.CertificateAuthorityPaths)
	}

	return &TLSConfig{
		FullChainCertPath:         tc.FullChainCertPath,
		PrivateKeyPath:            tc.PrivateKeyPath,
		ServeTLS:                  tc.ServeTLS,
		InsecureSkipVerify:        tc.InsecureSkipVerify,
		CertificateAuthorityPaths: caps,
		ClientCertPath:            tc.ClientCertPath,
		ClientKeyPath:             tc.ClientKeyPath,
	}
}

func (c *TricksterConfig) verifyTLSConfigs() error {

	for _, oc := range c.Origins {

		if oc.TLS == nil || (oc.TLS.FullChainCertPath == "" || oc.TLS.PrivateKeyPath == "") && (oc.TLS.CertificateAuthorityPaths == nil || len(oc.TLS.CertificateAuthorityPaths) == 0) {
			continue
		}

		_, err := ioutil.ReadFile(oc.TLS.FullChainCertPath)
		if err != nil {
			return err
		}
		_, err = ioutil.ReadFile(oc.TLS.PrivateKeyPath)
		if err != nil {
			return err
		}
		c.Frontend.ServeTLS = true
		oc.TLS.ServeTLS = true

		// Verify CA Paths
		if oc.TLS.CertificateAuthorityPaths != nil && len(oc.TLS.CertificateAuthorityPaths) > 0 {
			for _, path := range oc.TLS.CertificateAuthorityPaths {
				_, err = ioutil.ReadFile(path)
				if err != nil {
					return err
				}
			}
		}

	}
	return nil
}

// TLSCertConfig returns the crypto/tls configuration object with a list of name-bound certs derifed from the running config
func (c *TricksterConfig) TLSCertConfig() (*tls.Config, error) {
	var err error
	if !c.Frontend.ServeTLS {
		return nil, nil
	}
	to := []*OriginConfig{}
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

	i := 0
	for _, tc := range to {
		tlsConfig.Certificates[i], err = tls.LoadX509KeyPair(tc.TLS.FullChainCertPath, tc.TLS.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
		i++
	}

	tlsConfig.BuildNameToCertificate()

	return tlsConfig, nil

}
