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
	"testing"
	"time"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	testIssuer  = "le"
	testPath    = "/var/lib/trickster/acme"
	testCache   = "shared"
	testExample = "www.acme.test"
)

const fullYAML = `
wait_on_startup: 90s
storage:
  path: /var/lib/trickster/acme
issuers:
  le:
    email: ops@trickstercache.org
    agree_to_terms: true
    key_type: ECDSA-P384
    challenges: [HTTP-01]
    external_account_binding:
      key_id: kid
      hmac_key: abc
  dns:
    agree_to_terms: true
    directory_url: https://ca.acme.test/dir
    dns_provider:
      provider: Route53
      propagation_delay: 5s
on_demand:
  issuer: le
  listeners: [default]
  allowed_domains: ["*.Customers.acme.test"]
`

func validOptions() *Options {
	o := &Options{
		Storage: &StorageOptions{Path: testPath},
		Issuers: map[string]*IssuerOptions{testIssuer: {AgreeToTerms: true}},
	}
	o.Initialize()
	return o
}

func TestUnmarshalInitializeValidate(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(fullYAML), &o))
	o.Initialize()
	require.NoError(t, o.Validate())
	require.True(t, o.IsEnabled())
	le := o.Issuers[testIssuer]
	require.Equal(t, testIssuer, le.Name)
	require.Equal(t, DefaultDirectoryURL, le.DirectoryURL)
	require.Equal(t, KeyTypeECDSAP384, le.KeyType)
	require.Equal(t, []string{ChallengeHTTP01}, le.Challenges)
	require.Equal(t, []string{ChallengeDNS01}, o.Issuers["dns"].Challenges)
	require.Equal(t, DNSProviderRoute53, o.Issuers["dns"].DNSProvider.Provider)
	require.Equal(t, StorageFilesystem, o.Storage.Provider)
	require.Equal(t, timeconv.Duration(90*time.Second), o.WaitOnStartup)
	require.Equal(t, []string{"*.customers.acme.test"}, o.OnDemand.AllowedDomains)
	require.Equal(t, DefaultOnDemandRateLimit, o.OnDemand.RateLimit)
	require.Equal(t, DefaultOnDemandDecisionTTL, o.OnDemand.DecisionTTL)

	// the secret never leaves the process through a config dump
	out, err := yaml.Marshal(&o)
	require.NoError(t, err)
	require.NotContains(t, string(out), "hmac_key: abc")

	c := o.Clone()
	require.True(t, c.Equal(&o))
	c.Issuers[testIssuer].Challenges[0] = ChallengeTLSALPN01
	require.False(t, c.Equal(&o))
	require.Equal(t, ChallengeHTTP01, o.Issuers[testIssuer].Challenges[0])
}

func TestNilOptions(t *testing.T) {
	var o *Options
	require.False(t, o.IsEnabled())
	require.Nil(t, o.Clone())
	require.NoError(t, o.Validate())
	require.Empty(t, o.RedisCacheName())
	o.Initialize()
	require.True(t, o.Equal(nil))
	require.False(t, o.Equal(&Options{}))
	var s *StorageOptions
	require.Nil(t, s.Clone())
	var r *RedisStorageOptions
	require.Nil(t, r.Clone())
	var i *IssuerOptions
	require.Nil(t, i.Clone())
	require.False(t, i.HasChallenge(ChallengeHTTP01))
	var d *DNSProviderOptions
	require.Nil(t, d.Clone())
	var od *OnDemandOptions
	require.Nil(t, od.Clone())
	var b *BackendOptions
	require.Nil(t, b.Clone())
	require.True(t, b.Equal(nil))
}

func TestValidateErrors(t *testing.T) {
	tests := map[string]func(*Options){
		"no issuers":    func(o *Options) { o.Issuers = nil },
		"negative wait": func(o *Options) { o.WaitOnStartup = -1 },
		"no path":       func(o *Options) { o.Storage.Path = " " },
		"redis block on filesystem": func(o *Options) {
			o.Storage.Redis = &RedisStorageOptions{}
		},
		"unknown storage": func(o *Options) { o.Storage.Provider = "s3" },
		"redis both": func(o *Options) {
			o.Storage = &StorageOptions{Provider: StorageRedis, Redis: &RedisStorageOptions{
				CacheName: testCache, Connection: &redisopts.Options{},
			}}
			o.Storage.initialize()
		},
		"redis neither": func(o *Options) {
			o.Storage = &StorageOptions{Provider: StorageRedis}
			o.Storage.initialize()
		},
		"redis short ttl": func(o *Options) {
			o.Storage = &StorageOptions{Provider: StorageRedis, Redis: &RedisStorageOptions{
				CacheName: testCache, LockTTL: timeconv.Duration(time.Second),
			}}
		},
		"nil issuer":  func(o *Options) { o.Issuers["x"] = nil },
		"no terms":    func(o *Options) { o.Issuers[testIssuer].AgreeToTerms = false },
		"bad url":     func(o *Options) { o.Issuers[testIssuer].DirectoryURL = "ftp://x" },
		"bad key":     func(o *Options) { o.Issuers[testIssuer].KeyType = "dsa" },
		"bad chal":    func(o *Options) { o.Issuers[testIssuer].Challenges = []string{"smtp-01"} },
		"dup chal":    func(o *Options) { o.Issuers[testIssuer].Challenges = []string{ChallengeHTTP01, ChallengeHTTP01} },
		"dns mixed":   func(o *Options) { o.Issuers[testIssuer].Challenges = []string{ChallengeDNS01, ChallengeHTTP01} },
		"dns no prov": func(o *Options) { o.Issuers[testIssuer].Challenges = []string{ChallengeDNS01} },
		"prov no dns": func(o *Options) {
			o.Issuers[testIssuer].DNSProvider = &DNSProviderOptions{Provider: DNSProviderRoute53}
		},
		"eab no kid": func(o *Options) {
			o.Issuers[testIssuer].ExternalAccountBinding = &EABOptions{HMACKey: "k"}
		},
		"eab two keys": func(o *Options) {
			o.Issuers[testIssuer].ExternalAccountBinding = &EABOptions{KeyID: "k", HMACKey: "k", HMACKeyFile: "f"}
		},
		"on demand undefined issuer": func(o *Options) {
			o.OnDemand = &OnDemandOptions{Issuer: "x", Listeners: []string{"default"}, Ask: "http://a"}
		},
		"on demand no listeners": func(o *Options) {
			o.OnDemand = &OnDemandOptions{Issuer: testIssuer, Ask: "http://a"}
		},
		"on demand no gate": func(o *Options) {
			o.OnDemand = &OnDemandOptions{Issuer: testIssuer, Listeners: []string{"default"}}
		},
		"on demand bad ask": func(o *Options) {
			o.OnDemand = &OnDemandOptions{Issuer: testIssuer, Listeners: []string{"default"}, Ask: "nope"}
		},
		"on demand bad domain": func(o *Options) {
			o.OnDemand = &OnDemandOptions{
				Issuer: testIssuer, Listeners: []string{"default"},
				AllowedDomains: []string{"a.*.test"},
			}
		},
		"on demand negative": func(o *Options) {
			o.OnDemand = &OnDemandOptions{
				Issuer: testIssuer, Listeners: []string{"default"},
				Ask: "http://a", RateLimit: -1,
			}
		},
		"on demand dns issuer": func(o *Options) {
			o.Issuers[testIssuer].Challenges = []string{ChallengeDNS01}
			o.Issuers[testIssuer].DNSProvider = &DNSProviderOptions{Provider: DNSProviderRoute53}
			o.OnDemand = &OnDemandOptions{Issuer: testIssuer, Listeners: []string{"default"}, Ask: "http://a"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			o := validOptions()
			mutate(o)
			require.Error(t, o.Validate())
		})
	}
	require.NoError(t, validOptions().Validate())
}

func TestDNSProviderValidate(t *testing.T) {
	valid := []*DNSProviderOptions{
		{Provider: DNSProviderCloudflare, Cloudflare: &CloudflareOptions{APIToken: "t", ZoneTokenFile: "f"}},
		{Provider: DNSProviderRoute53},
		{Provider: DNSProviderRoute53, Route53: &Route53Options{AccessKeyID: "a", SecretAccessKeyFile: "f"}},
		{Provider: DNSProviderRFC2136, RFC2136: &RFC2136Options{Server: "s", KeyName: "k", KeyAlg: "a", Key: "x"}},
	}
	for _, o := range valid {
		require.NoError(t, o.validate(), o.Provider)
		require.True(t, o.Clone().Equal(o))
	}
	invalid := []*DNSProviderOptions{
		{Provider: DNSProviderCloudflare},
		{Provider: DNSProviderCloudflare, Cloudflare: &CloudflareOptions{APIToken: "t", APITokenFile: "f"}},
		{Provider: DNSProviderCloudflare, Cloudflare: &CloudflareOptions{
			APIToken: "t", ZoneToken: "z",
			ZoneTokenFile: "f",
		}},
		{Provider: DNSProviderRoute53, Route53: &Route53Options{SecretAccessKey: "s", SecretAccessKeyFile: "f"}},
		{Provider: DNSProviderRoute53, Route53: &Route53Options{AccessKeyID: "a"}},
		{Provider: DNSProviderRFC2136, RFC2136: &RFC2136Options{Server: "s"}},
		{Provider: DNSProviderRFC2136, RFC2136: &RFC2136Options{Server: "s", KeyName: "k", KeyAlg: "a"}},
		{Provider: "unknown"},
		{Provider: DNSProviderRoute53, PropagationDelay: -1},
		{Provider: DNSProviderRoute53, Cloudflare: &CloudflareOptions{APIToken: "t"}},
		{
			Provider: DNSProviderRoute53, Cloudflare: &CloudflareOptions{APIToken: "t"},
			RFC2136: &RFC2136Options{},
		},
	}
	for _, o := range invalid {
		require.Error(t, o.validate(), o.Provider)
	}
}

func TestStorageInitialize(t *testing.T) {
	s := &StorageOptions{Provider: " Redis "}
	s.initialize()
	require.Equal(t, StorageRedis, s.Provider)
	require.Equal(t, DefaultRedisKeyPrefix, s.Redis.KeyPrefix)
	require.Equal(t, DefaultRedisLockTTL, s.Redis.LockTTL)
	s.Redis.CacheName = testCache
	require.NoError(t, s.validate())
	o := &Options{Storage: s}
	require.Equal(t, testCache, o.RedisCacheName())
	require.Nil(t, o.RedisConnection())
	rc := &redisopts.Options{Endpoint: "127.0.0.1:6379"}
	require.Same(t, rc, (&Options{Storage: &StorageOptions{Redis: &RedisStorageOptions{Connection: rc}}}).RedisConnection())
	require.Nil(t, (*Options)(nil).RedisConnection())
	c := s.Clone()
	require.True(t, c.Equal(s))
	conn := &RedisStorageOptions{Connection: &redisopts.Options{Endpoints: []string{"a"}}}
	cc := conn.Clone()
	cc.Connection.Endpoints[0] = "b"
	require.Equal(t, "a", conn.Connection.Endpoints[0])
	require.False(t, cc.Equal(conn))
	require.False(t, conn.Equal(nil))
}

func TestBackendOptions(t *testing.T) {
	b := &BackendOptions{Issuer: testIssuer}
	got, err := b.ResolveDomains([]string{"WWW.acme.test.", "api.acme.test", "www.acme.test"})
	require.NoError(t, err)
	require.Equal(t, []string{"api.acme.test", testExample}, got)
	_, err = b.ResolveDomains(nil)
	require.ErrorIs(t, err, ErrNoDomains)
	b.Domains = []string{"*.acme.test"}
	got, err = b.ResolveDomains([]string{"ignored.acme.test"})
	require.NoError(t, err)
	require.Equal(t, []string{"*.acme.test"}, got)
	for _, bad := range []string{"", "**.acme.test", "localhost", "10.0.0.1", "a.*.test", "*.test"} {
		_, err := NormalizeDomain(bad)
		require.Error(t, err, bad)
	}
	b.ResolvedDomains = got
	c := b.Clone()
	require.True(t, c.Equal(b))
	c.Domains[0] = "x.acme.test"
	require.False(t, c.Equal(b))
	_, err = (&BackendOptions{Domains: []string{"bad host"}}).ResolveDomains(nil)
	require.Error(t, err)
}

func TestOnDemandOptions(t *testing.T) {
	o := &OnDemandOptions{
		Issuer: testIssuer, Listeners: []string{"default"},
		AllowedDomains: []string{"*.Acme.test", "bad host"},
	}
	o.initialize()
	require.Equal(t, []string{"*.acme.test", "bad host"}, o.AllowedDomains)
	c := o.Clone()
	require.True(t, c.Equal(o))
	c.Listeners[0] = "other"
	require.False(t, c.Equal(o))
	require.False(t, o.Equal(nil))
}
