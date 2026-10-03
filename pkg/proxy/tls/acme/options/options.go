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

// Package options defines the configuration for automatic certificate issuance and renewal
// through ACME (RFC 8555): the top-level acme section and the per-backend tls.acme opt-in.
package options

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/secret"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// ChallengeHTTP01 answers the CA over plain HTTP on port 80 (RFC 8555 section 8.3)
	ChallengeHTTP01 = "http-01"
	// ChallengeTLSALPN01 answers the CA during a TLS handshake on port 443 (RFC 8737)
	ChallengeTLSALPN01 = "tls-alpn-01"
	// ChallengeDNS01 answers the CA with a DNS TXT record (RFC 8555 section 8.4)
	ChallengeDNS01 = "dns-01"

	// StorageFilesystem keeps ACME accounts and certificates in a local directory
	StorageFilesystem = "filesystem"
	// StorageRedis keeps ACME accounts and certificates in Redis, shared by a cluster
	StorageRedis = "redis"

	// KeyTypeECDSAP256 is a certificate key on the NIST P-256 curve
	KeyTypeECDSAP256 = "ecdsa-p256"
	// KeyTypeECDSAP384 is a certificate key on the NIST P-384 curve
	KeyTypeECDSAP384 = "ecdsa-p384"
	// KeyTypeRSA2048 is a 2048-bit RSA certificate key
	KeyTypeRSA2048 = "rsa-2048"
	// KeyTypeRSA4096 is a 4096-bit RSA certificate key
	KeyTypeRSA4096 = "rsa-4096"
	// KeyTypeEd25519 is an Ed25519 certificate key, which not every CA or client accepts
	KeyTypeEd25519 = "ed25519"

	// DNSProviderCloudflare publishes dns-01 records through the Cloudflare API
	DNSProviderCloudflare = "cloudflare"
	// DNSProviderRoute53 publishes dns-01 records through the AWS Route 53 API
	DNSProviderRoute53 = "route53"
	// DNSProviderRFC2136 publishes dns-01 records with RFC 2136 dynamic updates
	DNSProviderRFC2136 = "rfc2136"

	// DefaultDirectoryURL is the Let's Encrypt production directory
	DefaultDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"
	// DefaultKeyType is the key type used when an issuer does not name one
	DefaultKeyType = KeyTypeECDSAP256
	// DefaultRedisKeyPrefix namespaces ACME storage keys in a shared Redis keyspace
	DefaultRedisKeyPrefix = "trickster:acme:"
	// DefaultRedisLockTTL bounds how long a crashed holder can block an ACME operation
	DefaultRedisLockTTL = timeconv.Duration(time.Minute)
	// MinimumRedisLockTTL is the shortest lock lease that survives a slow refresh
	MinimumRedisLockTTL = timeconv.Duration(5 * time.Second)
	// DefaultOnDemandDecisionTTL is how long an on-demand ask answer is reused
	DefaultOnDemandDecisionTTL = timeconv.Duration(5 * time.Minute)
	// DefaultOnDemandRateLimit is the most on-demand issuances started per minute
	DefaultOnDemandRateLimit = 10
	// DefaultOnDemandNegativeCacheSize bounds the names remembered as refused
	DefaultOnDemandNegativeCacheSize = 10000
)

var (
	// ErrTermsNotAccepted is returned when an issuer does not accept the CA's terms of service
	ErrTermsNotAccepted = errors.New("agree_to_terms must be true; Trickster never accepts terms implicitly")
	// ErrStoragePathRequired is returned when filesystem storage has no path
	ErrStoragePathRequired = errors.New("acme.storage.path is required for filesystem storage")
	// ErrNoIssuers is returned when the acme section declares no issuer
	ErrNoIssuers = errors.New("acme.issuers must declare at least one issuer")
	// ErrNegativeWait is returned when wait_on_startup is negative
	ErrNegativeWait = errors.New("acme.wait_on_startup must not be negative")
	// ErrDNSChallengeExclusive is returned when dns-01 is combined with another challenge
	ErrDNSChallengeExclusive = errors.New("dns-01 cannot be combined with another challenge type")
	// ErrNoDomains is returned when a backend opts in without any domain to request
	ErrNoDomains = errors.New("tls.acme needs domains, or hosts on its backend, to request certificates for")
)

// Options is the top-level acme configuration section
type Options struct {
	// Storage is where accounts, certificates and locks are kept
	Storage *StorageOptions `yaml:"storage,omitempty"`
	// Issuers maps an issuer name, referenced by backends, to its CA settings
	Issuers map[string]*IssuerOptions `yaml:"issuers,omitempty"`
	// WaitOnStartup holds readiness at startup until missing certificates are issued or it elapses
	WaitOnStartup timeconv.Duration `yaml:"wait_on_startup,omitempty"`
	// OnDemand enables issuance during the first handshake for names no backend lists
	OnDemand *OnDemandOptions `yaml:"on_demand,omitempty"`
}

// StorageOptions configures where ACME state is persisted
type StorageOptions struct {
	// Provider is filesystem (the default) or redis
	Provider string `yaml:"provider,omitempty"`
	// Path is the filesystem storage directory
	Path string `yaml:"path,omitempty"`
	// Redis configures Redis storage
	Redis *RedisStorageOptions `yaml:"redis,omitempty"`
}

// RedisStorageOptions configures Redis-backed ACME storage, which is never the cache's keyspace
type RedisStorageOptions struct {
	// CacheName borrows the connection settings of a named redis cache
	CacheName string `yaml:"cache_name,omitempty"`
	// Connection holds the connection settings when CacheName is not used
	Connection *redisopts.Options `yaml:"connection,omitempty"`
	// KeyPrefix namespaces every ACME key
	KeyPrefix string `yaml:"key_prefix,omitempty"`
	// LockTTL is the lease of a distributed lock, refreshed while it is held
	LockTTL timeconv.Duration `yaml:"lock_ttl,omitempty"`
}

// IssuerOptions configures one ACME CA and the account used with it
type IssuerOptions struct {
	// DirectoryURL is the CA's ACME directory endpoint
	DirectoryURL string `yaml:"directory_url,omitempty"`
	// Email is the account contact the CA sends expiry and policy notices to
	Email string `yaml:"email,omitempty"`
	// AgreeToTerms accepts the CA's subscriber agreement and must be set explicitly
	AgreeToTerms bool `yaml:"agree_to_terms,omitempty"`
	// Challenges lists the challenge types the issuer may use
	Challenges []string `yaml:"challenges,omitempty"`
	// KeyType is the certificate key algorithm
	KeyType string `yaml:"key_type,omitempty"`
	// Profile selects a certificate profile the CA advertises in its directory
	Profile string `yaml:"profile,omitempty"`
	// ExternalAccountBinding links the account to an existing CA account
	ExternalAccountBinding *EABOptions `yaml:"external_account_binding,omitempty"`
	// TrustedCAPaths are PEM files trusted for the CA's own HTTPS endpoint
	TrustedCAPaths []string `yaml:"trusted_ca_paths,omitempty"`
	// DNSProvider publishes dns-01 records
	DNSProvider *DNSProviderOptions `yaml:"dns_provider,omitempty"`
	// Name is the issuer's key in the issuers map
	Name string `yaml:"-"`
}

// EABOptions holds an External Account Binding (RFC 8555 section 7.3.4)
type EABOptions struct {
	// KeyID identifies the external account
	KeyID string `yaml:"key_id,omitempty"`
	// HMACKey is the base64url MAC key the CA issued
	HMACKey secret.Secret `yaml:"hmac_key,omitempty"`
	// HMACKeyFile is read for the MAC key in place of HMACKey
	HMACKeyFile string `yaml:"hmac_key_file,omitempty"`
}

// DNSProviderOptions configures the DNS API used to answer dns-01 challenges
type DNSProviderOptions struct {
	// Provider is cloudflare, route53 or rfc2136
	Provider string `yaml:"provider,omitempty"`
	// PropagationDelay waits this long after publishing before checking propagation
	PropagationDelay timeconv.Duration `yaml:"propagation_delay,omitempty"`
	// PropagationTimeout bounds the propagation check; a negative value skips it
	PropagationTimeout timeconv.Duration `yaml:"propagation_timeout,omitempty"`
	// Resolvers are the DNS servers (host:port) used for propagation checks
	Resolvers []string `yaml:"resolvers,omitempty"`
	// Cloudflare holds settings for the cloudflare provider
	Cloudflare *CloudflareOptions `yaml:"cloudflare,omitempty"`
	// Route53 holds settings for the route53 provider
	Route53 *Route53Options `yaml:"route53,omitempty"`
	// RFC2136 holds settings for the rfc2136 provider
	RFC2136 *RFC2136Options `yaml:"rfc2136,omitempty"`
}

// CloudflareOptions configures the cloudflare dns-01 provider
type CloudflareOptions struct {
	// APIToken needs Zone.DNS:Edit on the zones it serves
	APIToken secret.Secret `yaml:"api_token,omitempty"`
	// APITokenFile is read for the API token in place of APIToken
	APITokenFile string `yaml:"api_token_file,omitempty"`
	// ZoneToken optionally holds a separate Zone:Read token
	ZoneToken secret.Secret `yaml:"zone_token,omitempty"`
	// ZoneTokenFile is read for the zone token in place of ZoneToken
	ZoneTokenFile string `yaml:"zone_token_file,omitempty"`
}

// Route53Options configures the route53 dns-01 provider; unset credentials use the AWS default chain
type Route53Options struct {
	// Region is the AWS region of the API client
	Region string `yaml:"region,omitempty"`
	// Profile names a shared-config profile
	Profile string `yaml:"profile,omitempty"`
	// AccessKeyID is a static access key ID
	AccessKeyID string `yaml:"access_key_id,omitempty"`
	// SecretAccessKey is the static secret for AccessKeyID
	SecretAccessKey secret.Secret `yaml:"secret_access_key,omitempty"`
	// SecretAccessKeyFile is read for the secret in place of SecretAccessKey
	SecretAccessKeyFile string `yaml:"secret_access_key_file,omitempty"`
	// HostedZoneID pins the hosted zone instead of discovering it from the record name
	HostedZoneID string `yaml:"hosted_zone_id,omitempty"`
}

// RFC2136Options configures the rfc2136 dns-01 provider (BIND, PowerDNS, Knot and others)
type RFC2136Options struct {
	// Server is the authoritative server's host:port
	Server string `yaml:"server,omitempty"`
	// KeyName is the TSIG key name
	KeyName string `yaml:"key_name,omitempty"`
	// KeyAlg is the TSIG algorithm, such as hmac-sha256.
	KeyAlg string `yaml:"key_alg,omitempty"`
	// Key is the base64 TSIG secret
	Key secret.Secret `yaml:"key,omitempty"`
	// KeyFile is read for the TSIG secret in place of Key
	KeyFile string `yaml:"key_file,omitempty"`
}

// OnDemandOptions configures issuance during the first handshake for an unknown name
type OnDemandOptions struct {
	// Issuer names the issuer that on-demand certificates come from
	Issuer string `yaml:"issuer,omitempty"`
	// Listeners are the TLS listeners that issue on demand
	Listeners []string `yaml:"listeners,omitempty"`
	// Ask is a URL queried with ?domain=<name>; a 200 response permits issuance
	Ask string `yaml:"ask,omitempty"`
	// AllowedDomains are hostnames or wildcards a name must match to be issued
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
	// DecisionTTL is how long an ask answer, or a refusal, is remembered
	DecisionTTL timeconv.Duration `yaml:"decision_ttl,omitempty"`
	// RateLimit is the most on-demand issuances started per minute
	RateLimit int `yaml:"rate_limit,omitempty"`
	// NegativeCacheSize bounds the number of refused names remembered
	NegativeCacheSize int `yaml:"negative_cache_size,omitempty"`
}

// BackendOptions is a backend's tls.acme opt-in
type BackendOptions struct {
	// Issuer names the issuer the backend's certificates come from
	Issuer string `yaml:"issuer,omitempty"`
	// Domains to request certificates for; the backend's hosts when empty
	Domains []string `yaml:"domains,omitempty"`
	// ResolvedDomains is set by validation to the normalized names actually requested
	ResolvedDomains []string `yaml:"-"`
}

// IsEnabled reports whether the acme section declares anything to manage
func (o *Options) IsEnabled() bool {
	return o != nil && len(o.Issuers) > 0
}

// RedisCacheName returns the redis cache whose connection settings storage borrows, if any
func (o *Options) RedisCacheName() string {
	if o == nil || o.Storage == nil || o.Storage.Redis == nil {
		return ""
	}
	return o.Storage.Redis.CacheName
}

// RedisConnection returns the storage's own Redis connection settings, if any
func (o *Options) RedisConnection() *redisopts.Options {
	if o == nil || o.Storage == nil || o.Storage.Redis == nil {
		return nil
	}
	return o.Storage.Redis.Connection
}

// Clone returns a deep copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Storage = o.Storage.Clone()
	out.OnDemand = o.OnDemand.Clone()
	if o.Issuers != nil {
		out.Issuers = make(map[string]*IssuerOptions, len(o.Issuers))
		for k, v := range o.Issuers {
			out.Issuers[k] = v.Clone()
		}
	}
	return out
}

// Equal reports whether two Options are identical
func (o *Options) Equal(o2 *Options) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.WaitOnStartup == o2.WaitOnStartup && o.Storage.Equal(o2.Storage) &&
		o.OnDemand.Equal(o2.OnDemand) &&
		maps.EqualFunc(o.Issuers, o2.Issuers, (*IssuerOptions).Equal)
}

// Initialize applies defaults and records each issuer's name
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.Storage == nil {
		o.Storage = &StorageOptions{}
	}
	o.Storage.initialize()
	for name, iss := range o.Issuers {
		if iss == nil {
			continue
		}
		iss.Name = name
		iss.initialize()
	}
	if o.OnDemand != nil {
		o.OnDemand.initialize()
	}
}

// Validate checks the section on its own; references to listeners and caches are checked by the caller
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if len(o.Issuers) == 0 {
		return ErrNoIssuers
	}
	if o.WaitOnStartup < 0 {
		return ErrNegativeWait
	}
	if err := o.Storage.validate(); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(o.Issuers)) {
		iss := o.Issuers[name]
		if iss == nil {
			return fmt.Errorf("acme issuer %q is empty", name)
		}
		if err := iss.validate(); err != nil {
			return fmt.Errorf("acme issuer %q: %w", name, err)
		}
	}
	if o.OnDemand != nil {
		if err := o.OnDemand.validate(o.Issuers); err != nil {
			return fmt.Errorf("acme.on_demand: %w", err)
		}
	}
	return nil
}

// Clone returns a deep copy of the StorageOptions
func (o *StorageOptions) Clone() *StorageOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Redis = o.Redis.Clone()
	return out
}

// Equal reports whether two StorageOptions are identical
func (o *StorageOptions) Equal(o2 *StorageOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.Provider == o2.Provider && o.Path == o2.Path && o.Redis.Equal(o2.Redis)
}

func (o *StorageOptions) initialize() {
	o.Provider = strings.ToLower(strings.TrimSpace(o.Provider))
	if o.Provider == "" {
		o.Provider = StorageFilesystem
	}
	if o.Provider == StorageRedis {
		if o.Redis == nil {
			o.Redis = &RedisStorageOptions{}
		}
		if o.Redis.KeyPrefix == "" {
			o.Redis.KeyPrefix = DefaultRedisKeyPrefix
		}
		if o.Redis.LockTTL == 0 {
			o.Redis.LockTTL = DefaultRedisLockTTL
		}
	}
}

func (o *StorageOptions) validate() error {
	switch o.Provider {
	case StorageFilesystem:
		if strings.TrimSpace(o.Path) == "" {
			return ErrStoragePathRequired
		}
		if o.Redis != nil {
			return errors.New("acme.storage.redis is set but the storage provider is filesystem")
		}
	case StorageRedis:
		r := o.Redis
		if (r.CacheName == "") == (r.Connection == nil) {
			return errors.New("acme.storage.redis needs exactly one of cache_name or connection")
		}
		if r.LockTTL < MinimumRedisLockTTL {
			return fmt.Errorf("acme.storage.redis.lock_ttl must be at least %s",
				time.Duration(MinimumRedisLockTTL))
		}
	default:
		return fmt.Errorf("unknown acme storage provider %q", o.Provider)
	}
	return nil
}

// Clone returns a deep copy of the RedisStorageOptions
func (o *RedisStorageOptions) Clone() *RedisStorageOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	if o.Connection != nil {
		c := *o.Connection
		c.Endpoints = slices.Clone(o.Connection.Endpoints)
		out.Connection = &c
	}
	return out
}

// Equal reports whether two RedisStorageOptions are identical
func (o *RedisStorageOptions) Equal(o2 *RedisStorageOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.CacheName == o2.CacheName && o.KeyPrefix == o2.KeyPrefix &&
		o.LockTTL == o2.LockTTL && o.Connection.Equal(o2.Connection)
}

// Clone returns a deep copy of the IssuerOptions
func (o *IssuerOptions) Clone() *IssuerOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Challenges = slices.Clone(o.Challenges)
	out.TrustedCAPaths = slices.Clone(o.TrustedCAPaths)
	out.ExternalAccountBinding = pointers.Clone(o.ExternalAccountBinding)
	out.DNSProvider = o.DNSProvider.Clone()
	return out
}

// Equal reports whether two IssuerOptions are identical
func (o *IssuerOptions) Equal(o2 *IssuerOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.DirectoryURL == o2.DirectoryURL && o.Email == o2.Email &&
		o.AgreeToTerms == o2.AgreeToTerms && slices.Equal(o.Challenges, o2.Challenges) &&
		o.KeyType == o2.KeyType && o.Profile == o2.Profile &&
		slices.Equal(o.TrustedCAPaths, o2.TrustedCAPaths) &&
		equalPointers(o.ExternalAccountBinding, o2.ExternalAccountBinding) &&
		o.DNSProvider.Equal(o2.DNSProvider)
}

// HasChallenge reports whether the issuer may use the named challenge type
func (o *IssuerOptions) HasChallenge(challenge string) bool {
	return o != nil && slices.Contains(o.Challenges, challenge)
}

func (o *IssuerOptions) initialize() {
	if o.DirectoryURL == "" {
		o.DirectoryURL = DefaultDirectoryURL
	}
	if o.KeyType == "" {
		o.KeyType = DefaultKeyType
	}
	o.KeyType = strings.ToLower(o.KeyType)
	for i, c := range o.Challenges {
		o.Challenges[i] = strings.ToLower(strings.TrimSpace(c))
	}
	if len(o.Challenges) == 0 {
		if o.DNSProvider != nil {
			o.Challenges = []string{ChallengeDNS01}
		} else {
			o.Challenges = []string{ChallengeHTTP01, ChallengeTLSALPN01}
		}
	}
	if o.DNSProvider != nil {
		o.DNSProvider.Provider = strings.ToLower(strings.TrimSpace(o.DNSProvider.Provider))
	}
}

func (o *IssuerOptions) validate() error {
	if !o.AgreeToTerms {
		return ErrTermsNotAccepted
	}
	u, err := url.Parse(o.DirectoryURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid directory_url %q", o.DirectoryURL)
	}
	switch o.KeyType {
	case KeyTypeECDSAP256, KeyTypeECDSAP384, KeyTypeRSA2048, KeyTypeRSA4096, KeyTypeEd25519:
	default:
		return fmt.Errorf("unknown key_type %q", o.KeyType)
	}
	seen := make(map[string]struct{}, len(o.Challenges))
	for _, c := range o.Challenges {
		switch c {
		case ChallengeHTTP01, ChallengeTLSALPN01, ChallengeDNS01:
		default:
			return fmt.Errorf("unknown challenge %q", c)
		}
		if _, ok := seen[c]; ok {
			return fmt.Errorf("challenge %q is listed twice", c)
		}
		seen[c] = struct{}{}
	}
	if o.HasChallenge(ChallengeDNS01) {
		if len(o.Challenges) > 1 {
			return ErrDNSChallengeExclusive
		}
		if o.DNSProvider == nil {
			return errors.New("dns-01 requires dns_provider")
		}
	} else if o.DNSProvider != nil {
		return errors.New("dns_provider is set but challenges does not include dns-01")
	}
	if eab := o.ExternalAccountBinding; eab != nil {
		if eab.KeyID == "" {
			return errors.New("external_account_binding.key_id is required")
		}
		if (eab.HMACKey == "") == (eab.HMACKeyFile == "") {
			return errors.New("external_account_binding needs exactly one of hmac_key or hmac_key_file")
		}
	}
	if o.DNSProvider != nil {
		if err := o.DNSProvider.validate(); err != nil {
			return fmt.Errorf("dns_provider: %w", err)
		}
	}
	return nil
}

// Clone returns a deep copy of the DNSProviderOptions
func (o *DNSProviderOptions) Clone() *DNSProviderOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Resolvers = slices.Clone(o.Resolvers)
	out.Cloudflare = pointers.Clone(o.Cloudflare)
	out.Route53 = pointers.Clone(o.Route53)
	out.RFC2136 = pointers.Clone(o.RFC2136)
	return out
}

// Equal reports whether two DNSProviderOptions are identical
func (o *DNSProviderOptions) Equal(o2 *DNSProviderOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.Provider == o2.Provider && o.PropagationDelay == o2.PropagationDelay &&
		o.PropagationTimeout == o2.PropagationTimeout && slices.Equal(o.Resolvers, o2.Resolvers) &&
		equalPointers(o.Cloudflare, o2.Cloudflare) && equalPointers(o.Route53, o2.Route53) &&
		equalPointers(o.RFC2136, o2.RFC2136)
}

func (o *DNSProviderOptions) validate() error {
	if o.PropagationDelay < 0 {
		return errors.New("propagation_delay must not be negative")
	}
	var configured int
	for _, set := range []bool{o.Cloudflare != nil, o.Route53 != nil, o.RFC2136 != nil} {
		if set {
			configured++
		}
	}
	if configured > 1 {
		return errors.New("only the block for the named provider may be set")
	}
	switch o.Provider {
	case DNSProviderCloudflare:
		c := o.Cloudflare
		if c == nil || (c.APIToken == "") == (c.APITokenFile == "") {
			return errors.New("cloudflare needs exactly one of api_token or api_token_file")
		}
		if c.ZoneToken != "" && c.ZoneTokenFile != "" {
			return errors.New("cloudflare accepts only one of zone_token or zone_token_file")
		}
	case DNSProviderRoute53:
		// without a route53 block, the AWS default credential chain is used
		if r := o.Route53; r != nil {
			if r.SecretAccessKey != "" && r.SecretAccessKeyFile != "" {
				return errors.New("route53 accepts only one of secret_access_key or secret_access_key_file")
			}
			hasSecret := r.SecretAccessKey != "" || r.SecretAccessKeyFile != ""
			if (r.AccessKeyID != "") != hasSecret {
				return errors.New("route53 access_key_id and its secret must be set together")
			}
		}
	case DNSProviderRFC2136:
		r := o.RFC2136
		if r == nil || r.Server == "" || r.KeyName == "" || r.KeyAlg == "" {
			return errors.New("rfc2136 needs server, key_name and key_alg")
		}
		if (r.Key == "") == (r.KeyFile == "") {
			return errors.New("rfc2136 needs exactly one of key or key_file")
		}
	default:
		return fmt.Errorf("unknown dns provider %q", o.Provider)
	}
	if o.Cloudflare != nil && o.Provider != DNSProviderCloudflare ||
		o.Route53 != nil && o.Provider != DNSProviderRoute53 ||
		o.RFC2136 != nil && o.Provider != DNSProviderRFC2136 {
		return fmt.Errorf("settings for a provider other than %q are set", o.Provider)
	}
	return nil
}

// Clone returns a deep copy of the OnDemandOptions
func (o *OnDemandOptions) Clone() *OnDemandOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Listeners = slices.Clone(o.Listeners)
	out.AllowedDomains = slices.Clone(o.AllowedDomains)
	return out
}

// Equal reports whether two OnDemandOptions are identical
func (o *OnDemandOptions) Equal(o2 *OnDemandOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.Issuer == o2.Issuer && slices.Equal(o.Listeners, o2.Listeners) &&
		o.Ask == o2.Ask && slices.Equal(o.AllowedDomains, o2.AllowedDomains) &&
		o.DecisionTTL == o2.DecisionTTL && o.RateLimit == o2.RateLimit &&
		o.NegativeCacheSize == o2.NegativeCacheSize
}

func (o *OnDemandOptions) initialize() {
	if o.DecisionTTL == 0 {
		o.DecisionTTL = DefaultOnDemandDecisionTTL
	}
	if o.RateLimit == 0 {
		o.RateLimit = DefaultOnDemandRateLimit
	}
	if o.NegativeCacheSize == 0 {
		o.NegativeCacheSize = DefaultOnDemandNegativeCacheSize
	}
	for i, d := range o.AllowedDomains {
		if n, err := hostnames.Normalize(d, hostnames.RequireHost); err == nil {
			o.AllowedDomains[i] = n
		}
	}
}

func (o *OnDemandOptions) validate(issuers map[string]*IssuerOptions) error {
	iss, ok := issuers[o.Issuer]
	if !ok {
		return fmt.Errorf("issuer %q is not defined", o.Issuer)
	}
	if iss.HasChallenge(ChallengeDNS01) {
		return errors.New("on-demand issuance cannot use a dns-01 issuer")
	}
	if len(o.Listeners) == 0 {
		return errors.New("listeners must name at least one TLS listener")
	}
	if o.Ask == "" && len(o.AllowedDomains) == 0 {
		return errors.New("ask or allowed_domains is required to gate issuance")
	}
	if o.Ask != "" {
		u, err := url.Parse(o.Ask)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid ask url %q", o.Ask)
		}
	}
	for _, d := range o.AllowedDomains {
		if _, err := hostnames.Normalize(d, hostnames.RequireHost); err != nil {
			return fmt.Errorf("allowed_domains entry %q: %w", d, err)
		}
	}
	if o.DecisionTTL < 0 || o.RateLimit < 0 || o.NegativeCacheSize < 0 {
		return errors.New("decision_ttl, rate_limit and negative_cache_size must not be negative")
	}
	return nil
}

// Clone returns a deep copy of the BackendOptions
func (o *BackendOptions) Clone() *BackendOptions {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Domains = slices.Clone(o.Domains)
	out.ResolvedDomains = slices.Clone(o.ResolvedDomains)
	return out
}

// Equal reports whether two BackendOptions are identical
func (o *BackendOptions) Equal(o2 *BackendOptions) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.Issuer == o2.Issuer && slices.Equal(o.Domains, o2.Domains)
}

// ResolveDomains returns the normalized, sorted and deduplicated names to request, defaulting to hosts
func (o *BackendOptions) ResolveDomains(hosts []string) ([]string, error) {
	in := o.Domains
	if len(in) == 0 {
		in = hosts
	}
	if len(in) == 0 {
		return nil, ErrNoDomains
	}
	out := make([]string, 0, len(in))
	for _, d := range in {
		n, err := NormalizeDomain(d)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// NormalizeDomain lowercases a certificate name, refusing ones no certificate can carry
func NormalizeDomain(d string) (string, error) {
	n, err := hostnames.Normalize(d, hostnames.RequireHost)
	if err != nil {
		return "", fmt.Errorf("acme domain %q: %w", d, err)
	}
	if net.ParseIP(n) != nil {
		return "", fmt.Errorf("acme domain %q: IP address certificates are not supported", d)
	}
	if hostnames.IsAnyDepth(n) {
		return "", fmt.Errorf("acme domain %q: a certificate wildcard covers exactly one label; "+
			"list the names in tls.acme.domains", d)
	}
	if !strings.Contains(hostnames.Suffix(n), ".") {
		return "", fmt.Errorf("acme domain %q: a certificate name needs at least two labels", d)
	}
	return n, nil
}

func equalPointers[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
