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

package acme

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"

	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"

	"github.com/caddyserver/certmagic"
	"github.com/mholt/acmez/v3/acme"
	"go.uber.org/zap"
)

type issuerFactory func(cfg *certmagic.Config, o *acmeopts.IssuerOptions, ports issuerPorts,
	log *zap.Logger) (certmagic.Issuer, *certmagic.ACMEIssuer, error)

type issuer struct {
	name   string
	opts   *acmeopts.IssuerOptions
	ports  issuerPorts
	cfg    *certmagic.Config
	acme   *certmagic.ACMEIssuer
	ctx    context.Context
	cancel context.CancelFunc
}

var keyTypes = map[string]certmagic.KeyType{
	acmeopts.KeyTypeECDSAP256: certmagic.P256,
	acmeopts.KeyTypeECDSAP384: certmagic.P384,
	acmeopts.KeyTypeRSA2048:   certmagic.RSA2048,
	acmeopts.KeyTypeRSA4096:   certmagic.RSA4096,
	acmeopts.KeyTypeEd25519:   certmagic.ED25519,
}

func newACMEIssuer(cfg *certmagic.Config, o *acmeopts.IssuerOptions, ports issuerPorts,
	log *zap.Logger,
) (certmagic.Issuer, *certmagic.ACMEIssuer, error) {
	tmpl := certmagic.ACMEIssuer{
		CA:                      o.DirectoryURL,
		Email:                   o.Email,
		Agreed:                  o.AgreeToTerms,
		Profile:                 o.Profile,
		DisableHTTPChallenge:    !o.HasChallenge(acmeopts.ChallengeHTTP01) || ports.http <= 0,
		DisableTLSALPNChallenge: !o.HasChallenge(acmeopts.ChallengeTLSALPN01) || ports.tls <= 0,
		ListenHost:              ports.host,
		AltHTTPPort:             ports.http,
		AltTLSALPNPort:          ports.tls,
		Logger:                  log,
	}
	if eab := o.ExternalAccountBinding; eab != nil {
		key, err := readSecret(eab.HMACKey, eab.HMACKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("external_account_binding: %w", err)
		}
		tmpl.ExternalAccount = &acme.EAB{KeyID: eab.KeyID, MACKey: key}
	}
	if len(o.TrustedCAPaths) > 0 {
		pool, err := trustedRoots(o.TrustedCAPaths)
		if err != nil {
			return nil, nil, err
		}
		tmpl.TrustedRoots = pool
	}
	if o.DNSProvider != nil {
		solver, err := newDNSSolver(o.DNSProvider, log)
		if err != nil {
			return nil, nil, fmt.Errorf("dns_provider: %w", err)
		}
		tmpl.DNS01Solver = solver
	}
	iss := certmagic.NewACMEIssuer(cfg, tmpl)
	return iss, iss, nil
}

func trustedRoots(paths []string) (*x509.CertPool, error) {
	// the system roots stay trusted alongside every certificate in paths
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, p := range paths {
		b, err := os.ReadFile(p) // #nosec G304 -- path comes from operator-provided config
		if err != nil {
			return nil, fmt.Errorf("trusted_ca_paths: %w", err)
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("trusted_ca_paths: %s holds no PEM certificate", p)
		}
	}
	return pool, nil
}
