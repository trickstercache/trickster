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

	"github.com/BurntSushi/toml"
)

// TLSConfig is a collection of TLS configurations
type TLSConfig struct {
	// FullChainCertPath specifies the path of the file containing the
	// concatenated server certification and the intermediate certification for the tls endpoint
	FullChainCertPath string `toml:"full_chain_cert_path"`
	// PrivateKeyPath specifies the path of the private key file for the tls endpoint
	PrivateKeyPath string `toml:"private_key_path"`
}

// DefaultTLSConfig will return a *TLSConfig with the default settings
func DefaultTLSConfig() *TLSConfig {
	return &TLSConfig{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

func (c *TricksterConfig) verifyTLSConfigs() error {
	for _, tc := range c.TLS {
		if tc.FullChainCertPath != "" {
			_, err := ioutil.ReadFile(tc.FullChainCertPath)
			if err != nil {
				return err
			}
		}
		if tc.PrivateKeyPath != "" {
			_, err := ioutil.ReadFile(tc.PrivateKeyPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *TricksterConfig) processTLSConfigs(metadata toml.MetaData) {
	for k, v := range c.TLS {
		if _, ok := c.activeTLS[k]; !ok {
			// a configured cache was not used by any origin. don't even instantiate it
			delete(c.TLS, k)
			continue
		}

		//cc := DefaultCachingConfig()
		tc := DefaultTLSConfig()

		if metadata.IsDefined("tls", k, "full_chain_cert_path") {
			tc.FullChainCertPath = v.FullChainCertPath
		}

		if metadata.IsDefined("tls", k, "private_key_path") {
			tc.PrivateKeyPath = v.PrivateKeyPath
		}
		c.TLS[k] = tc
	}
}

// TLSCertConfig returns the crypto/tls configuration object with a list of name-bound certs from the config
func TLSCertConfig() (*tls.Config, error) {

	var err error

	l := len(TLS)
	if l == 0 {
		return nil, nil
	}

	tlsConfig := &tls.Config{}
	tlsConfig.Certificates = make([]tls.Certificate, l)

	i := 0
	for _, tc := range TLS {
		tlsConfig.Certificates[i], err = tls.LoadX509KeyPair(tc.FullChainCertPath, tc.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
	}

	tlsConfig.BuildNameToCertificate()

	return tlsConfig, nil

}

// TLSListener returns an TLS Listener based on the Trickster Config
func TLSListener() (net.Listener, error) {
	tlsConfig, err := TLSCertConfig()
	if err != nil {
		return nil, err
	}
	return tls.Listen("tcp",
		fmt.Sprintf("%s:%d",
			ProxyServer.TLSListenAddress,
			ProxyServer.TLSListenPort,
		),
		tlsConfig,
	)
}
