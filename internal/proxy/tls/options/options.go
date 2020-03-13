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

// Package tls handles options for TLS (https) requests
package options

import (
	"io/ioutil"
)

// Options is a collection of TLS-related client and server configurations
type Options struct {
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

// NewOptions will return a *Options with the default settings
func NewOptions() *Options {
	return &Options{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

// Clone returns an exact copy of the subject *Options
func (o *Options) Clone() *Options {

	var caps []string
	if o.CertificateAuthorityPaths != nil {
		caps = make([]string, len(o.CertificateAuthorityPaths))
		copy(caps, o.CertificateAuthorityPaths)
	}

	return &Options{
		FullChainCertPath:         o.FullChainCertPath,
		PrivateKeyPath:            o.PrivateKeyPath,
		ServeTLS:                  o.ServeTLS,
		InsecureSkipVerify:        o.InsecureSkipVerify,
		CertificateAuthorityPaths: caps,
		ClientCertPath:            o.ClientCertPath,
		ClientKeyPath:             o.ClientKeyPath,
	}
}

func (o *Options) Verify() error {

	if (o.FullChainCertPath == "" || o.PrivateKeyPath == "") && (o.CertificateAuthorityPaths == nil || len(o.CertificateAuthorityPaths) == 0) {
		return nil
	}

	_, err := ioutil.ReadFile(o.FullChainCertPath)
	if err != nil {
		return err
	}
	_, err = ioutil.ReadFile(o.PrivateKeyPath)
	if err != nil {
		return err
	}

	// TODO: RELOCATE
	// c.Frontend.ServeTLS = true
	o.ServeTLS = true

	// Verify CA Paths
	if o.CertificateAuthorityPaths != nil && len(o.CertificateAuthorityPaths) > 0 {
		for _, path := range o.CertificateAuthorityPaths {
			_, err = ioutil.ReadFile(path)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
