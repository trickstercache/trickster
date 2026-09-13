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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
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
	// CertificateAuthorityPEM is an inline PEM bundle of additional Certificate
	// Authorities for the upstream origin, for material that arrives as
	// configuration rather than as a file
	CertificateAuthorityPEM string `yaml:"certificate_authority_pem,omitempty"`
	// ServerName is the hostname sent as SNI and verified against the upstream
	// origin's certificate, when it differs from the origin_url host
	ServerName string `yaml:"server_name,omitempty"`
	// ExcludeSystemRoots makes the configured Certificate Authorities the
	// only ones trusted for the upstream origin, rather than additions to
	// the operating system's; it requires at least one to be configured
	ExcludeSystemRoots bool `yaml:"exclude_system_roots,omitempty"`
}

var _ types.ConfigOptions[Options] = &Options{}

// New will return a *Options with the default settings
func New() *Options {
	return &Options{
		FullChainCertPath: "",
		PrivateKeyPath:    "",
	}
}

// Clone returns an exact copy of the subject *Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	out.CertificateAuthorityPaths = slices.Clone(o.CertificateAuthorityPaths)
	return out
}

// Equal returns true if all exposed option members are equal
func (o *Options) Equal(o2 *Options) bool {
	return o.FullChainCertPath == o2.FullChainCertPath &&
		o.PrivateKeyPath == o2.PrivateKeyPath &&
		o.InsecureSkipVerify == o2.InsecureSkipVerify &&
		slices.Equal(o.CertificateAuthorityPaths, o2.CertificateAuthorityPaths) &&
		o.ClientCertPath == o2.ClientCertPath &&
		o.ClientKeyPath == o2.ClientKeyPath &&
		o.CertificateAuthorityPEM == o2.CertificateAuthorityPEM &&
		o.ServerName == o2.ServerName &&
		o.ExcludeSystemRoots == o2.ExcludeSystemRoots
}

// ErrInvalidCertificateAuthorityPEM is returned when certificate_authority_pem
// holds no parsable certificate
var ErrInvalidCertificateAuthorityPEM = errors.New(
	"certificate_authority_pem holds no parsable certificate")

// ErrExcludeSystemRootsWithoutCAs is returned when exclude_system_roots is set
// with no Certificate Authority configured, which would trust nothing at all
var ErrExcludeSystemRootsWithoutCAs = errors.New(
	"exclude_system_roots requires certificate_authority_paths or certificate_authority_pem")

func (o *Options) Initialize(_ string) error {
	// ServeTLS indicates this backend participates in the frontend's TLS
	// listener by presenting a server certificate. Only a full server
	// cert+key pair enables that. CertificateAuthorityPaths alone is used
	// for verifying peers on outbound connections (mTLS) and must NOT
	// cascade into flipping Frontend.ServeTLS — see #940.
	if o.FullChainCertPath != "" && o.PrivateKeyPath != "" {
		o.ServeTLS = true
	}
	return nil
}

// Validate returns true if the TLS Options are validated
func (o *Options) Validate() (bool, error) {
	if o.CertificateAuthorityPEM != "" {
		if _, err := ValidateCABundle([]byte(o.CertificateAuthorityPEM)); err != nil {
			return false, err
		}
	}
	if o.ExcludeSystemRoots && len(o.CertificateAuthorityPaths) == 0 &&
		o.CertificateAuthorityPEM == "" {
		return false, ErrExcludeSystemRootsWithoutCAs
	}
	if (o.FullChainCertPath == "" || o.PrivateKeyPath == "") &&
		len(o.CertificateAuthorityPaths) == 0 &&
		o.CertificateAuthorityPEM == "" && o.ServerName == "" {
		return false, nil
	}
	if o.FullChainCertPath != "" && o.PrivateKeyPath != "" {
		if _, err := os.ReadFile(o.FullChainCertPath); err != nil {
			return false, err
		}
		if _, err := os.ReadFile(o.PrivateKeyPath); err != nil {
			return false, err
		}
	}
	if len(o.CertificateAuthorityPaths) > 0 {
		for _, path := range o.CertificateAuthorityPaths {
			if _, err := os.ReadFile(path); err != nil {
				return false, err
			}
		}
	}
	if o.ClientCertPath != "" {
		if _, err := os.ReadFile(o.ClientCertPath); err != nil {
			return false, err
		}
	}
	if o.ClientKeyPath != "" {
		if _, err := os.ReadFile(o.ClientKeyPath); err != nil {
			return false, err
		}
	}
	return true, nil
}

// ToClientTLSConfig renders the client-side portion of the Options into a
// *tls.Config for outbound connections: the mutual-auth client certificate
// pair, any additional Certificate Authorities to trust alongside the
// system pool, and the InsecureSkipVerify escape hatch. The server-side
// fields (FullChainCertPath, PrivateKeyPath, ServeTLS) are not consulted.
//
// A nil receiver yields a nil config, which net/http reads as "default TLS",
// so callers may pass an absent TLS block straight through.
func (o *Options) ToClientTLSConfig() (*tls.Config, error) {
	if o == nil {
		return nil, nil
	}
	// #nosec G402 -- InsecureSkipVerify is a documented, operator-set option
	out := &tls.Config{InsecureSkipVerify: o.InsecureSkipVerify, ServerName: o.ServerName}
	if o.ClientCertPath != "" && o.ClientKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(o.ClientCertPath, o.ClientKeyPath)
		if err != nil {
			return nil, err
		}
		out.Certificates = []tls.Certificate{cert}
	}
	if len(o.CertificateAuthorityPaths) == 0 && o.CertificateAuthorityPEM == "" {
		return out, nil
	}
	// start from the system pool so configured CAs are additive rather than
	// replacing public trust, unless the configuration says they are the
	// whole of it; an unavailable system pool degrades to an empty one,
	// which trusts exactly the configured CAs
	var rootCAs *x509.CertPool
	if !o.ExcludeSystemRoots {
		rootCAs, _ = x509.SystemCertPool()
	}
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	for _, path := range o.CertificateAuthorityPaths {
		certs, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, err := ValidateCABundle(certs); err != nil {
			return nil, fmt.Errorf("unable to append to CA Certs from file %s: %w", path, err)
		}
		rootCAs.AppendCertsFromPEM(certs)
	}
	if o.CertificateAuthorityPEM != "" {
		if _, err := ValidateCABundle([]byte(o.CertificateAuthorityPEM)); err != nil {
			return nil, err
		}
		rootCAs.AppendCertsFromPEM([]byte(o.CertificateAuthorityPEM))
	}
	out.RootCAs = rootCAs
	return out, nil
}

func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}

// ValidateCABundle parses a PEM bundle of certificates and returns how many
// it holds. A bundle holding no certificate, or a certificate block that
// does not parse, is an error; blocks of other types are ignored.
func ValidateCABundle(bundle []byte) (int, error) {
	var n int
	for len(bundle) > 0 {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrInvalidCertificateAuthorityPEM, err)
		}
		n++
	}
	if n == 0 {
		return 0, ErrInvalidCertificateAuthorityPEM
	}
	return n, nil
}
