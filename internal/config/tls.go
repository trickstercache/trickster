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
	"fmt"
	"io/ioutil"
	"net"
)

// TLSConfig is a collection of TLS-related client and server configurations
type TLSConfig struct {
	// FullChainCertPath specifies the path of the file containing the
	// concatenated server certification and the intermediate certification for the tls endpoint
	FullChainCertPath string `toml:"full_chain_cert_path"`
	// PrivateKeyPath specifies the path of the private key file for the tls endpoint
	PrivateKeyPath string `toml:"private_key_path"`

	// SkipVerify indicates that the HTTPS Client in Trickster should bypass
	// hostname verification for the origin's certificate when proxying requests
	SkipVerify bool `toml:"skip_verify"`
	// CertificateAuthorities provides a list of custom Certificate Authorities for the upstream origin
	// which are considered in addition to any system CA's
	CertificateAuthorityPaths []string `toml:"certificate_authority_paths"`
}

// DefaultTLSConfig will return a *TLSConfig with the default settings
func DefaultTLSConfig() *TLSConfig {
	return &TLSConfig{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

func (c *TricksterConfig) verifyTLSConfigs() error {

	for _, oc := range c.Origins {

		if oc.TLS == nil || (oc.TLS.FullChainCertPath == "" || oc.TLS.PrivateKeyPath == "") && (oc.TLS.CertificateAuthorityPaths != nil || len(oc.TLS.CertificateAuthorityPaths) == 0) {
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
		c.ProxyServer.ServeTLS = true
		oc.ServeTLS = true

		// Verify CA Paths
		if oc.TLS.CertificateAuthorityPaths != nil || len(oc.TLS.CertificateAuthorityPaths) > 0 {
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
	if !c.ProxyServer.ServeTLS {
		return nil, nil
	}
	to := []*OriginConfig{}
	for _, oc := range c.Origins {
		if oc.ServeTLS {
			to = append(to, oc)
		}
	}

	l := len(to)
	if l == 0 {
		return nil, nil
	}

	tlsConfig := &tls.Config{}
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

// TLSListener returns an TLS Listener based on the Trickster Config
func (c *TricksterConfig) TLSListener() (net.Listener, error) {
	tlsConfig, err := c.TLSCertConfig()
	if err != nil {
		return nil, err
	}
	return tls.Listen("tcp",
		fmt.Sprintf("%s:%d",
			c.ProxyServer.TLSListenAddress,
			c.ProxyServer.TLSListenPort,
		),
		tlsConfig,
	)
}
