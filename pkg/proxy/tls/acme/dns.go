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
	"fmt"
	"os"
	"strings"
	"time"

	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/rfc2136"
	"github.com/libdns/route53"
	"go.uber.org/zap"
)

const skipPropagationCheck time.Duration = -1

func readSecret(value secret.Secret, file string) (string, error) {
	if value != "" || file == "" {
		return string(value), nil
	}
	b, err := os.ReadFile(file) // #nosec G304 -- path comes from operator-provided config
	if err != nil {
		return "", fmt.Errorf("reading secret file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func newDNSProvider(o *acmeopts.DNSProviderOptions) (certmagic.DNSProvider, error) {
	switch o.Provider {
	case acmeopts.DNSProviderCloudflare:
		token, err := readSecret(o.Cloudflare.APIToken, o.Cloudflare.APITokenFile)
		if err != nil {
			return nil, err
		}
		zoneToken, err := readSecret(o.Cloudflare.ZoneToken, o.Cloudflare.ZoneTokenFile)
		if err != nil {
			return nil, err
		}
		return &cloudflare.Provider{APIToken: token, ZoneToken: zoneToken}, nil
	case acmeopts.DNSProviderRoute53:
		p := &route53.Provider{WaitForRoute53Sync: true}
		if r := o.Route53; r != nil {
			secretKey, err := readSecret(r.SecretAccessKey, r.SecretAccessKeyFile)
			if err != nil {
				return nil, err
			}
			p.Region, p.Profile, p.HostedZoneID = r.Region, r.Profile, r.HostedZoneID
			p.AccessKeyId, p.SecretAccessKey = r.AccessKeyID, secretKey
		}
		return p, nil
	case acmeopts.DNSProviderRFC2136:
		key, err := readSecret(o.RFC2136.Key, o.RFC2136.KeyFile)
		if err != nil {
			return nil, err
		}
		return &rfc2136.Provider{
			Server: o.RFC2136.Server, KeyName: o.RFC2136.KeyName,
			KeyAlg: o.RFC2136.KeyAlg, Key: key,
		}, nil
	}
	return nil, fmt.Errorf("unknown dns provider %q", o.Provider)
}

func newDNSSolver(o *acmeopts.DNSProviderOptions, log *zap.Logger) (*certmagic.DNS01Solver, error) {
	p, err := newDNSProvider(o)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(o.PropagationTimeout)
	if timeout < 0 {
		// the library skips propagation checks only for exactly -1
		timeout = skipPropagationCheck
	}
	return &certmagic.DNS01Solver{
		DNSProvider:        p,
		PropagationDelay:   time.Duration(o.PropagationDelay),
		PropagationTimeout: timeout,
		Resolvers:          o.Resolvers,
		Logger:             log,
	}, nil
}
