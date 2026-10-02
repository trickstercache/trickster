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
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cacheopts "github.com/trickstercache/trickster/v2/pkg/cache/options"
	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/alicebob/miniredis/v2"
	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/rfc2136"
	"github.com/libdns/route53"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	testSecret     = "s3cret"
	testSecretFile = "secret.txt"
	missingFile    = "/nonexistent/trickster/acme/secret"
)

func writeSecretFile(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), testSecretFile)
	require.NoError(t, os.WriteFile(p, []byte(value+"\n"), 0o600))
	return p
}

func TestReadSecret(t *testing.T) {
	v, err := readSecret(testSecret, "")
	require.NoError(t, err)
	require.Equal(t, testSecret, v)
	v, err = readSecret("", writeSecretFile(t, testSecret))
	require.NoError(t, err)
	require.Equal(t, testSecret, v)
	_, err = readSecret("", missingFile)
	require.Error(t, err)
	v, err = readSecret("", "")
	require.NoError(t, err)
	require.Empty(t, v)
}

func TestNewDNSProvider(t *testing.T) {
	file := writeSecretFile(t, testSecret)
	p, err := newDNSProvider(&acmeopts.DNSProviderOptions{
		Provider:   acmeopts.DNSProviderCloudflare,
		Cloudflare: &acmeopts.CloudflareOptions{APITokenFile: file, ZoneToken: "zone"},
	})
	require.NoError(t, err)
	require.Equal(t, &cloudflare.Provider{APIToken: testSecret, ZoneToken: "zone"}, p)

	p, err = newDNSProvider(&acmeopts.DNSProviderOptions{
		Provider: acmeopts.DNSProviderRoute53,
		Route53: &acmeopts.Route53Options{
			Region: "us-east-1", AccessKeyID: "AKID",
			SecretAccessKey: secret.Secret(testSecret), HostedZoneID: "Z1",
		},
	})
	require.NoError(t, err)
	r53 := p.(*route53.Provider)
	require.Equal(t, "us-east-1", r53.Region)
	require.Equal(t, testSecret, r53.SecretAccessKey)
	require.Equal(t, "Z1", r53.HostedZoneID)
	require.True(t, r53.WaitForRoute53Sync)

	p, err = newDNSProvider(&acmeopts.DNSProviderOptions{Provider: acmeopts.DNSProviderRoute53})
	require.NoError(t, err)
	require.Empty(t, p.(*route53.Provider).AccessKeyId)

	p, err = newDNSProvider(&acmeopts.DNSProviderOptions{
		Provider: acmeopts.DNSProviderRFC2136,
		RFC2136: &acmeopts.RFC2136Options{
			Server: "127.0.0.1:53", KeyName: "k", KeyAlg: "hmac-sha256",
			KeyFile: file,
		},
	})
	require.NoError(t, err)
	require.Equal(t, &rfc2136.Provider{
		Server: "127.0.0.1:53", KeyName: "k", KeyAlg: "hmac-sha256",
		Key: testSecret,
	}, p)

	for _, o := range []*acmeopts.DNSProviderOptions{
		{Provider: acmeopts.DNSProviderCloudflare, Cloudflare: &acmeopts.CloudflareOptions{APITokenFile: missingFile}},
		{Provider: acmeopts.DNSProviderCloudflare, Cloudflare: &acmeopts.CloudflareOptions{
			APIToken:      "t",
			ZoneTokenFile: missingFile,
		}},
		{Provider: acmeopts.DNSProviderRoute53, Route53: &acmeopts.Route53Options{
			AccessKeyID:         "a",
			SecretAccessKeyFile: missingFile,
		}},
		{Provider: acmeopts.DNSProviderRFC2136, RFC2136: &acmeopts.RFC2136Options{KeyFile: missingFile}},
		{Provider: "unknown"},
	} {
		_, err := newDNSProvider(o)
		require.Error(t, err, o.Provider)
	}
}

func TestNewDNSSolver(t *testing.T) {
	o := &acmeopts.DNSProviderOptions{
		Provider:           acmeopts.DNSProviderRoute53,
		PropagationTimeout: timeconv.Duration(-time.Second), PropagationDelay: timeconv.Duration(time.Second),
		Resolvers: []string{"127.0.0.1:53"},
	}
	s, err := newDNSSolver(o, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, skipPropagationCheck, s.PropagationTimeout)
	require.Equal(t, time.Second, s.PropagationDelay)
	o.PropagationTimeout = timeconv.Duration(time.Minute)
	s, err = newDNSSolver(o, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, time.Minute, s.PropagationTimeout)
	_, err = newDNSSolver(&acmeopts.DNSProviderOptions{Provider: "unknown"}, zap.NewNop())
	require.Error(t, err)
}

func TestNewACMEIssuer(t *testing.T) {
	stub := newStubIssuer(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: stub.caDER}), 0o600))
	cfg := certmagic.New(certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return nil, nil },
	}), certmagic.Config{})
	o := &acmeopts.IssuerOptions{
		DirectoryURL: "https://ca.acme.test/dir", Email: "ops@trickstercache.org",
		AgreeToTerms: true, Challenges: []string{acmeopts.ChallengeHTTP01}, Profile: "shortlived",
		TrustedCAPaths: []string{caFile},
		ExternalAccountBinding: &acmeopts.EABOptions{
			KeyID:       "kid",
			HMACKeyFile: writeSecretFile(t, testSecret),
		},
	}
	ports := issuerPorts{host: "127.0.0.1", http: 80, tls: 443}
	ci, ai, err := newACMEIssuer(cfg, o, ports, zap.NewNop())
	require.NoError(t, err)
	require.Same(t, ci, ai)
	require.Equal(t, "https://ca.acme.test/dir", ai.CA)
	require.False(t, ai.DisableHTTPChallenge)
	require.True(t, ai.DisableTLSALPNChallenge)
	require.Equal(t, 80, ai.AltHTTPPort)
	require.Equal(t, "127.0.0.1", ai.ListenHost)
	require.Equal(t, testSecret, ai.ExternalAccount.MACKey)
	require.NotNil(t, ai.TrustedRoots)
	require.Equal(t, "shortlived", ai.Profile)

	// a challenge whose port no listener holds is disabled rather than bound by the library
	_, ai, err = newACMEIssuer(cfg, o, issuerPorts{tls: 443}, zap.NewNop())
	require.NoError(t, err)
	require.True(t, ai.DisableHTTPChallenge)

	dnsOpts := o.Clone()
	dnsOpts.ExternalAccountBinding, dnsOpts.TrustedCAPaths = nil, nil
	dnsOpts.Challenges = []string{acmeopts.ChallengeDNS01}
	dnsOpts.DNSProvider = &acmeopts.DNSProviderOptions{Provider: acmeopts.DNSProviderRoute53}
	_, ai, err = newACMEIssuer(cfg, dnsOpts, ports, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, ai.DNS01Solver)

	for name, mutate := range map[string]func(*acmeopts.IssuerOptions){
		"eab file":    func(o *acmeopts.IssuerOptions) { o.ExternalAccountBinding.HMACKeyFile = missingFile },
		"ca file":     func(o *acmeopts.IssuerOptions) { o.TrustedCAPaths = []string{missingFile} },
		"ca contents": func(o *acmeopts.IssuerOptions) { o.TrustedCAPaths = []string{writeSecretFile(t, "x")} },
		"dns": func(o *acmeopts.IssuerOptions) {
			o.DNSProvider = &acmeopts.DNSProviderOptions{Provider: "unknown"}
		},
	} {
		bad := o.Clone()
		mutate(bad)
		_, _, err := newACMEIssuer(cfg, bad, ports, zap.NewNop())
		require.Error(t, err, name)
	}
}

func TestNewStorage(t *testing.T) {
	s, closeFn, err := newStorage(&acmeopts.StorageOptions{
		Provider: acmeopts.StorageFilesystem,
		Path:     t.TempDir(),
	}, nil)
	require.NoError(t, err)
	require.IsType(t, &certmagic.FileStorage{}, s)
	require.NoError(t, closeFn())

	_, _, err = newStorage(nil, nil)
	require.ErrorIs(t, err, acmeopts.ErrStoragePathRequired)
	redisOpts := &acmeopts.StorageOptions{
		Provider: acmeopts.StorageRedis,
		Redis: &acmeopts.RedisStorageOptions{
			KeyPrefix: acmeopts.DefaultRedisKeyPrefix,
			LockTTL:   acmeopts.DefaultRedisLockTTL,
		},
	}
	_, _, err = newStorage(redisOpts, nil)
	require.Error(t, err)

	mr := miniredis.RunT(t)
	s, closeFn, err = newStorage(redisOpts, &redisopts.Options{Endpoint: mr.Addr()})
	require.NoError(t, err)
	require.NoError(t, s.Store(t.Context(), "k", []byte("v")))
	require.NoError(t, closeFn())

	// an unreachable server is retried by the client rather than refused
	addr := mr.Addr()
	mr.Close()
	s, closeFn, err = newStorage(redisOpts, &redisopts.Options{Endpoint: addr})
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NoError(t, closeFn())
}

func TestRedisConnection(t *testing.T) {
	conf := testConfig(t, t.TempDir())
	require.Nil(t, redisConnection(conf))
	conn := &redisopts.Options{Endpoint: "127.0.0.1:6379"}
	conf.ACME.Storage = &acmeopts.StorageOptions{
		Provider: acmeopts.StorageRedis,
		Redis:    &acmeopts.RedisStorageOptions{Connection: conn},
	}
	require.Same(t, conn, redisConnection(conf))
	borrowed := &redisopts.Options{Endpoint: "127.0.0.1:6380"}
	conf.Caches = cacheopts.Lookup{"shared": &cacheopts.Options{Redis: borrowed}}
	conf.ACME.Storage.Redis = &acmeopts.RedisStorageOptions{CacheName: "shared"}
	require.Same(t, borrowed, redisConnection(conf))
	conf.ACME.Storage.Redis.CacheName = "missing"
	require.Nil(t, redisConnection(conf))
}

func TestNewPlan(t *testing.T) {
	require.Empty(t, newPlan(nil).domainIssuer)
	conf := testConfig(t, t.TempDir(), testDomain, testDomain2)
	other := listener.New("other")
	other.ListenPort, other.TLSListenPort, other.ServeTLS = 9080, 9443, true
	other.ListenAddress, other.TLSListenAddress = "10.0.0.1", "10.0.0.2"
	plaintextOnly := listener.New("plain")
	plaintextOnly.ListenPort = 9180
	conf.Listeners["other"], conf.Listeners["plain"] = other, plaintextOnly
	b := conf.Backends[testBackend]
	b.ListenerNames = []string{testListener, "other", "plain", "missing"}
	b.TLS.ACME.ResolvedDomains = []string{testDomain}
	tmpl := bo.New()
	tmpl.IsTemplate = true
	tmpl.TLS = &to.Options{ACME: &acmeopts.BackendOptions{Issuer: testIssuer}}
	badHosts := bo.New()
	badHosts.Hosts = []string{"**.acme.test"}
	badHosts.ListenerNames = []string{testListener}
	badHosts.TLS = &to.Options{ACME: &acmeopts.BackendOptions{Issuer: testIssuer}}
	conf.Backends["template"], conf.Backends["bad"] = tmpl, badHosts
	conf.ACME.OnDemand = &acmeopts.OnDemandOptions{
		Issuer:    testIssuer,
		Listeners: []string{"other", "other", "plain"},
	}

	p := newPlan(conf)
	require.Equal(t, map[string]string{testDomain: testIssuer}, p.domainIssuer)
	require.Equal(t, map[string][]string{testListener: {testDomain}, "other": {testDomain}}, p.listenerDomains)
	require.Equal(t, issuerPorts{host: "127.0.0.1", http: testHTTPPort, tls: testTLSPort},
		p.issuerPorts[testIssuer])
	require.Equal(t, []string{"other"}, p.onDemand)

	// a TLS-only first listener names its own address; differing addresses fall back to all
	tlsOnly := &plan{issuerPorts: map[string]issuerPorts{}}
	tlsOnly.addPorts(testIssuer, &listener.Options{TLSListenAddress: "10.0.0.3", TLSListenPort: 443})
	require.Equal(t, issuerPorts{host: "10.0.0.3", tls: 443}, tlsOnly.issuerPorts[testIssuer])
	mixed := &plan{issuerPorts: map[string]issuerPorts{}}
	mixed.addPorts(testIssuer, other)
	require.Equal(t, issuerPorts{http: 9080, tls: 9443}, mixed.issuerPorts[testIssuer])
}

func TestZapCore(t *testing.T) {
	prev := logger.Level()
	t.Cleanup(func() { logger.SetLogLevel(prev) })
	logger.SetLogLevel(level.Warn)
	core := &zapCore{}
	require.False(t, core.Enabled(zapcore.InfoLevel))
	require.True(t, core.Enabled(zapcore.ErrorLevel))
	for l, want := range map[zapcore.Level]level.Level{
		zapcore.DebugLevel: level.Debug, zapcore.InfoLevel: level.Debug,
		zapcore.WarnLevel: level.Warn, zapcore.ErrorLevel: level.Error, zapcore.DPanicLevel: level.Error,
	} {
		require.Equal(t, want, tricksterLevel(l))
	}
	child := core.With([]zapcore.Field{zap.String("a", "b")}).(*zapCore)
	require.Len(t, child.fields, 1)
	require.Empty(t, core.fields)
	ce := child.Check(zapcore.Entry{Level: zapcore.ErrorLevel}, nil)
	require.NotNil(t, ce)
	require.Nil(t, child.Check(zapcore.Entry{Level: zapcore.DebugLevel}, nil))
	require.NoError(t, child.Write(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "m", LoggerName: "n"},
		[]zapcore.Field{zap.Int("c", 1)}))
	require.NoError(t, child.Sync())
	newZapLogger().Warn("acme adapter test", zap.String("k", "v"))
}
