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

package options

import (
	"os"

	"github.com/trickstercache/trickster/v2/pkg/util/copiers"
	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// Options is a collection of TLS-related client and server configurations
type Options struct {
	// FullChainCertPath specifies the path of the file containing the
	// concatenated server certification and the intermediate certification for the tls endpoint
	FullChainCertPath string `yaml:"full_chain_cert_path,omitempty"`
	// PrivateKeyPath specifies the path of the private key file for the tls endpoint
	PrivateKeyPath string `yaml:"private_key_path,omitempty"`
	// ServeTLS is set to true once the Cert and Key files have been validated,
	// indicating the consumer of this config can service requests over TLS
	ServeTLS bool `yaml:"-"`
	// InsecureSkipVerify indicates that the HTTPS Client in Trickster should bypass
	// hostname verification for the origin's certificate when proxying requests
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`
	// CertificateAuthorities provides a list of custom Certificate Authorities for the upstream origin
	// which are considered in addition to any system CA's by the Trickster HTTPS Client
	CertificateAuthorityPaths []string `yaml:"certificate_authority_paths,omitempty"`
	// ClientCertPath provides the path to the Client Certificate when using Mutual Authorization
	ClientCertPath string `yaml:"client_cert_path,omitempty"`
	// ClientKeyPath provides the path to the Client Key when using Mutual Authorization
	ClientKeyPath string `yaml:"client_key_path,omitempty"`
}

// New will return a *Options with the default settings
func New() *Options {
	return &Options{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

// Clone returns an exact copy of the subject *Options
func (o *Options) Clone() *Options {
	return &Options{
		FullChainCertPath:         o.FullChainCertPath,
		PrivateKeyPath:            o.PrivateKeyPath,
		ServeTLS:                  o.ServeTLS,
		InsecureSkipVerify:        o.InsecureSkipVerify,
		CertificateAuthorityPaths: copiers.CopyStrings(o.CertificateAuthorityPaths),
		ClientCertPath:            o.ClientCertPath,
		ClientKeyPath:             o.ClientKeyPath,
	}
}

// Equal returns true if all exposed option members are equal
func (o *Options) Equal(o2 *Options) bool {
	return o.FullChainCertPath == o2.FullChainCertPath &&
		o.PrivateKeyPath == o2.PrivateKeyPath &&
		o.InsecureSkipVerify == o2.InsecureSkipVerify &&
		strutil.Equal(o.CertificateAuthorityPaths, o2.CertificateAuthorityPaths) &&
		o.ClientCertPath == o2.ClientCertPath &&
		o.ClientKeyPath == o2.ClientKeyPath
}

// Validate returns true if the TLS Options are validated
func (o *Options) Validate() (bool, error) {

	if (o.FullChainCertPath == "" || o.PrivateKeyPath == "") &&
		(o.CertificateAuthorityPaths == nil || len(o.CertificateAuthorityPaths) == 0) {
		return false, nil
	}

	_, err := os.ReadFile(o.FullChainCertPath)
	if err != nil {
		return false, err
	}
	_, err = os.ReadFile(o.PrivateKeyPath)
	if err != nil {
		return false, err
	}

	// Verify CA Paths
	if o.CertificateAuthorityPaths != nil && len(o.CertificateAuthorityPaths) > 0 {
		for _, path := range o.CertificateAuthorityPaths {
			_, err = os.ReadFile(path)
			if err != nil {
				return false, err
			}
		}
	}

	o.ServeTLS = true

	return true, nil
}
