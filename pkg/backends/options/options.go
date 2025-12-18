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
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/common/sigv4"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/tree"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"gopkg.in/yaml.v2"
)

var restrictedOriginNames = sets.New([]string{"", "frontend"})

// Lookup is a map of Options
type Lookup map[string]*Options

// Options is a collection of configurations for Trickster backends
type Options struct {
	// HTTP and Proxy Configurations
	//
	// Hosts identifies the frontend hostnames this backend should handle (virtual hosting)
	Hosts []string `yaml:"hosts,omitempty"`
	// Provider describes the type of backend (e.g., 'prometheus')
	Provider string `yaml:"provider,omitempty"`
	// OriginURL provides the base upstream URL for all proxied requests to this Backend.
	// it can be as simple as http://example.com or as complex as https://example.com:8443/path/prefix
	OriginURL string `yaml:"origin_url,omitempty"`
	// Timeout defines how long the HTTP request will wait for a response before timing out
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// KeepAliveTimeout defines how long an open keep-alive HTTP connection remains idle before closing
	KeepAliveTimeout time.Duration `yaml:"keep_alive_timeout,omitempty"`
	// MaxConcurrentConns defines maximum number of open concurrent connections to maintain to the backend
	MaxConcurrentConns int `yaml:"max_concurrent_conns,omitempty"`
	// MaxIdleConns defines maximum number of open keep-alive connections to maintain
	MaxIdleConns int `yaml:"max_idle_conns,omitempty"`
	// CacheName provides the name of the configured cache where the backend client will store it's cache data
	CacheName string `yaml:"cache_name,omitempty"`
	// CacheKeyPrefix defines the cache key prefix the backend will use when writing objects to the cache
	CacheKeyPrefix string `yaml:"cache_key_prefix,omitempty"`
	// ChunkReadConcurrencyLimit defines the concurrency limit while reading a chunked object
	ChunkReadConcurrencyLimit int `yaml:"chunk_read_concurrency_limit,omitempty"`
	// ChunkWriteConcurrencyLimit defines the concurrency limit while writing a chunked object
	ChunkWriteConcurrencyLimit int `yaml:"chunk_write_concurrency_limit,omitempty"`
	// HealthCheck is the health check options reference for this backend
	HealthCheck *ho.Options `yaml:"healthcheck,omitempty"`
	// Object Proxy Cache and Delta Proxy Cache Configurations
	// TimeseriesRetentionFactor limits the maximum the number of chronological
	// timestamps worth of data to store in cache for each query
	TimeseriesRetentionFactor int `yaml:"timeseries_retention_factor,omitempty"`
	// TimeseriesEvictionMethodName specifies which methodology ("oldest", "lru") is used to identify
	// timeseries to evict from a full cache object
	TimeseriesEvictionMethodName string `yaml:"timeseries_eviction_method,omitempty"`
	// BackfillTolerance prevents values with timestamps newer than the provided number of
	// milliseconds from being cached. this allows propagation of upstream backfill operations
	// that modify recently-cached data
	BackfillTolerance time.Duration `yaml:"backfill_tolerance,omitempty"`
	// BackfillTolerancePoints is similar to the MS version, except that it's final value is dependent
	// on the query step value to determine the relative duration of backfill tolerance per-query
	// When both are set, the higher of the two values is used
	BackfillTolerancePoints int `yaml:"backfill_tolerance_points,omitempty"`
	// Paths is a list of Path Options that control the behavior of the given paths when requested
	Paths po.List `yaml:"paths,omitempty"`
	// NegativeCacheName provides the name of the Negative Cache Config to be used by this Backend
	NegativeCacheName string `yaml:"negative_cache_name,omitempty"`
	// TimeseriesTTL specifies the cache TTL of timeseries objects
	TimeseriesTTL time.Duration `yaml:"timeseries_ttl,omitempty"`
	// TimeseriesTTLMS specifies the cache TTL of fast forward data
	FastForwardTTL time.Duration `yaml:"fastforward_ttl,omitempty"`
	// MaxTTL specifies the maximum allowed TTL for any cache object
	MaxTTL time.Duration `yaml:"max_ttl,omitempty"`
	// RevalidationFactor specifies how many times to multiply the object freshness lifetime
	// by to calculate an absolute cache TTL
	RevalidationFactor float64 `yaml:"revalidation_factor,omitempty"`
	// MaxObjectSizeBytes specifies the max objectsize to be accepted for any given cache object
	MaxObjectSizeBytes int `yaml:"max_object_size_bytes,omitempty"`
	// CompressibleTypeList specifies the HTTP Object Content Types that will be compressed internally
	// when stored in the Trickster cache or served to clients with a compatible 'Accept-Encoding' header
	CompressibleTypeList []string `yaml:"compressible_types,omitempty"`
	// TracingConfigName provides the name of the Tracing Config to be used by this Backend
	TracingConfigName string `yaml:"tracing_name,omitempty"`
	// RuleName provides the name of the rule config to be used by this backend.
	// This is only effective if the Backend provider is 'rule'
	RuleName string `yaml:"rule_name,omitempty"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request prior to
	// processing by the backend client
	ReqRewriterName string `yaml:"req_rewriter_name,omitempty"`
	// MaxShardSizePoints defines the maximum size of a timeseries request in unique timestamps,
	// before sharding into multiple requests of this denomination and reconsitituting the results.
	// If MaxShardSizePoints and MaxShardSizeMS are both > 0, the configuration is invalid
	MaxShardSizePoints int `yaml:"shard_max_size_points,omitempty"`
	// MaxShardSizeTime defines the max size of a timeseries request,
	// before sharding into multiple requests of this denomination and reconsitituting the results.
	// If MaxShardSizePoints and MaxShardSizeTime are both > 0, the configuration is invalid
	MaxShardSizeTime time.Duration `yaml:"shard_max_size_time,omitempty"`
	// ShardStep defines the epoch-aligned cadence to use when creating shards. When set to 0,
	// shards are not aligned to the epoch at a specific step. MaxShardSizeMS must be perfectly
	// divisible by ShardStep when both are > 0, or the configuration is invalid
	ShardStep time.Duration `yaml:"shard_step,omitempty"`
	// ProxyOnly, when true, will cause this backend to bypass caching while handling the request
	ProxyOnly bool `yaml:"proxy_only,omitempty"`

	// ALBOptions holds the options for ALBs
	ALBOptions *ao.Options `yaml:"alb,omitempty"`
	// Prometheus holds options specific to prometheus backends
	Prometheus *prop.Options `yaml:"prometheus,omitempty"`

	// TLS is the TLS Configuration for the Frontend and Backend
	TLS *to.Options `yaml:"tls,omitempty"`

	// ForwardedHeaders indicates the class of 'Forwarded' header to attach to upstream requests
	ForwardedHeaders string `yaml:"forwarded_headers,omitempty"`

	// IsDefault indicates if this is the d.Default backend for any request not matching a configured route
	IsDefault bool `yaml:"is_default,omitempty"`
	// FastForwardDisable indicates whether the FastForward feature should be disabled for this backend
	FastForwardDisable bool `yaml:"fast_forward_disable,omitempty"`
	// PathRoutingDisabled, when true, will bypass /backendName/path route registrations
	PathRoutingDisabled bool `yaml:"path_routing_disabled,omitempty"`
	// RequireTLS, when true, indicates this Backend Config's paths must only be registered with the TLS Router
	RequireTLS bool `yaml:"require_tls,omitempty"`
	// MultipartRangesDisabled, when true, indicates that if a downstream client requests multiple ranges
	// in a single request, Trickster will instead request and return a 200 OK with the full object body
	MultipartRangesDisabled bool `yaml:"multipart_ranges_disabled,omitempty"`
	// DearticulateUpstreamRanges, when true, indicates that when Trickster requests multiple ranges from
	// the backend, that they be requested as individual upstream requests instead of a single request that
	// expects a multipart response	// this optimizes Trickster to request as few bytes as possible when
	// fronting backends that only support single range requests
	DearticulateUpstreamRanges bool `yaml:"dearticulate_upstream_ranges,omitempty"`
	// Authentication
	// AuthenticatorName specifies the name of the optional Authenticator to attach to this Backend, and
	// can be overridden at the Path level.
	AuthenticatorName string `yaml:"authenticator_name,omitempty"`
	// AWS SigV4
	SigV4 *sigv4.SigV4Config `yaml:"sigv4,omitempty"`

	// Simulated Latency
	// When LatencyMin > 0 and LatencyMaxMS < LatencyMin (e.g., 0), then LatencyMin of latency
	// are applied to the request. When LatencyMaxMS > LatencyMin, then a random amount of
	// latency between the two values will be applied to the request
	//
	// LatencyMin is the minimum amount of simulated latency to apply to each incoming request
	LatencyMin time.Duration `yaml:"latency_min"`
	// LatencyMax is the maximum amount of simulated latency to apply to each incoming request
	LatencyMax time.Duration `yaml:"latency_max"`

	// Synthesized Configurations
	// These configurations are parsed versions of those defined above, and are what Trickster uses internally
	//
	// Name is the Name of the backend, taken from the Key in the Backends Lookup Map
	Name string `yaml:"-"`
	// Router is a router.Router containing this backend's Path Routes; it is set during route registration
	Router router.Router `yaml:"-"`
	// Scheme is the layer 7 protocol indicator (e.g. 'http'), derived from OriginURL
	Scheme string `yaml:"-"`
	// Host is the upstream hostname/IP[:port] the backend client will connect to when fetching uncached data,
	// derived from OriginURL
	Host string `yaml:"-"`
	// PathPrefix provides any prefix added to the front of the requested path when constructing the upstream
	// request url, derived from OriginURL
	PathPrefix string `yaml:"-"`
	// NegativeCache provides a map for the negative cache, with TTLs converted to time.Durations
	NegativeCache negative.Lookup `yaml:"-"`
	// TimeseriesRetention when subtracted from time.Now() represents the oldest allowable timestamp in a
	// timeseries when EvictionMethod is 'oldest'
	TimeseriesRetention time.Duration `yaml:"-"`
	// TimeseriesEvictionMethod is the parsed value of TimeseriesEvictionMethodName
	TimeseriesEvictionMethod evictionmethods.TimeseriesEvictionMethod `yaml:"-"`
	// FastForwardPath is the paths.Options to use for upstream Fast Forward Requests
	FastForwardPath *po.Options `yaml:"-"`
	// HTTPClient is the Client used by Trickster to communicate with the origin
	HTTPClient *http.Client `yaml:"-"`
	// CompressibleTypes is the map version of CompressibleTypeList for fast lookup
	CompressibleTypes sets.Set[string] `yaml:"-"`
	// RuleOptions is the reference to the Rule Options as indicated by RuleName
	RuleOptions *ro.Options `yaml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions `yaml:"-"`
	// AuthOptions is the authenticator as indicated by AuthenticatorName
	AuthOptions *autho.Options `yaml:"-"`
	// DoesShard is true when sharding will be used with this origin, based on how the
	// sharding options have been configured
	DoesShard bool `yaml:"-"`
}

var _ types.ConfigOptions[Options] = &Options{}

// New will return a pointer to a Backend Options with the default configuration settings
func New() *Options {
	return &Options{
		BackfillTolerance:            DefaultBackfillTolerance,
		BackfillTolerancePoints:      DefaultBackfillTolerancePoints,
		CacheKeyPrefix:               "",
		CacheName:                    DefaultBackendCacheName,
		CompressibleTypeList:         DefaultCompressibleTypes(),
		ChunkReadConcurrencyLimit:    DefaultChunkReadConcurrencyLimit,
		ChunkWriteConcurrencyLimit:   DefaultChunkWriteConcurrencyLimit,
		FastForwardTTL:               DefaultFastForwardTTL,
		ForwardedHeaders:             DefaultForwardedHeaders,
		HealthCheck:                  ho.New(),
		KeepAliveTimeout:             DefaultKeepAliveTimeout,
		MaxIdleConns:                 DefaultMaxIdleConns,
		MaxConcurrentConns:           DefaultMaxConcurrentConns,
		MaxObjectSizeBytes:           DefaultMaxObjectSizeBytes,
		MaxTTL:                       DefaultMaxTTL,
		NegativeCache:                make(map[int]time.Duration),
		NegativeCacheName:            DefaultBackendNegativeCacheName,
		Paths:                        make(po.List, 0, 10),
		RevalidationFactor:           DefaultRevalidationFactor,
		MaxShardSizePoints:           DefaultTimeseriesShardSize,
		MaxShardSizeTime:             DefaultTimeseriesShardSize,
		ShardStep:                    DefaultTimeseriesShardStep,
		TLS:                          &to.Options{},
		Timeout:                      DefaultBackendTimeout,
		TimeseriesEvictionMethod:     DefaultBackendTEM,
		TimeseriesEvictionMethodName: DefaultBackendTEMName,
		TimeseriesRetention:          DefaultBackendTRF,
		TimeseriesRetentionFactor:    DefaultBackendTRF,
		TimeseriesTTL:                DefaultTimeseriesTTL,
		TracingConfigName:            DefaultTracingConfigName,
	}
}

// Clone returns an exact copy of an *backends.Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	if o.HealthCheck != nil {
		out.HealthCheck = o.HealthCheck.Clone()
	}
	out.Hosts = slices.Clone(o.Hosts)
	out.CompressibleTypeList = slices.Clone(o.CompressibleTypeList)
	if o.CompressibleTypes != nil {
		out.CompressibleTypes = maps.Clone(o.CompressibleTypes)
	}
	if o.Paths != nil {
		out.Paths = o.Paths.Clone()
	}
	if o.NegativeCache != nil {
		out.NegativeCache = maps.Clone(o.NegativeCache)
	}
	if o.TLS != nil {
		out.TLS = o.TLS.Clone()
	}

	if o.FastForwardPath != nil {
		out.FastForwardPath = o.FastForwardPath.Clone()
	}

	if o.RuleOptions != nil {
		out.RuleOptions = o.RuleOptions.Clone()
	}

	if o.ALBOptions != nil {
		out.ALBOptions = o.ALBOptions.Clone()
	}

	if o.Prometheus != nil {
		out.Prometheus = o.Prometheus.Clone()
	}

	if o.AuthOptions != nil {
		out.AuthOptions = o.AuthOptions.Clone()
	}

	return out
}

// Validate validates the Backend Options
func (o *Options) Validate() (bool, error) {
	if err := ValidateBackendName(o.Name); err != nil {
		return false, err
	}
	if o.Provider == "" {
		return false, NewErrMissingProvider(o.Name)
	}
	if !providers.NonOriginBackends().Contains(o.Provider) && o.OriginURL == "" {
		return false, NewErrMissingOriginURL(o.Name)
	}
	if o.OriginURL != "" {
		if _, err := url.Parse(o.OriginURL); err != nil {
			return false, fmt.Errorf("invalid origin_url for backend %s: %w", o.Name, err)
		}
	}
	if o.MaxShardSizeTime > 0 && o.MaxShardSizePoints > 0 {
		return false, ErrInvalidMaxShardSize
	}

	if o.ShardStep > 0 && o.MaxShardSizeTime > 0 && o.MaxShardSizeTime%o.ShardStep != 0 {
		return false, ErrInvalidMaxShardSizeTime
	}

	if len(o.Paths) > 0 {
		if err := o.Paths.Validate(); err != nil {
			return false, err
		}
	}

	if o.HealthCheck != nil {
		_, err := o.HealthCheck.Validate()
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

// Validate validates the Lookup collection of Backend Options
func (l Lookup) Validate() error {
	backendTree := make(tree.Entries, len(l))
	var k int
	for key, o := range l {
		if o == nil {
			continue
		}
		o.Name = key
		_, err := o.Validate()
		if err != nil {
			return err
		}
		entry := &tree.Entry{
			Name: key,
			Type: o.Provider,
		}
		if o.ALBOptions != nil {
			if len(o.ALBOptions.Pool) > 0 {
				entry.Pool = o.ALBOptions.Pool
			} else if o.ALBOptions.UserRouter != nil {
				used := sets.NewStringSet()
				if o.ALBOptions.UserRouter.DefaultBackend != "" {
					used.Set(o.ALBOptions.UserRouter.DefaultBackend)
				}
				for _, u := range o.ALBOptions.UserRouter.Users {
					if u.ToBackend != "" && !used.Contains(u.ToBackend) {
						used.Set(u.ToBackend)
					}
				}
				if len(used) > 0 {
					entry.UserRouterPool = used.Keys()
				}
			}
		}
		backendTree[k] = entry
		k++
	}
	backendTree = backendTree[:k]
	// this checks for infinite routing loops and other non-obvious config issues
	if err := backendTree.Validate(); err != nil {
		return err
	}
	// this checks the validator for any targetTypes which should be passed on
	// to a userRouter
	for _, e := range backendTree {
		if e.TargetProvider == "" {
			continue
		}
		o, ok := l[e.Name]
		if !ok || o == nil || o.ALBOptions == nil || o.ALBOptions.UserRouter == nil {
			continue
		}
		o.ALBOptions.UserRouter.TargetProvider = e.TargetProvider
	}
	return backendTree[:k].Validate()
}

// ValidateBackendName ensures the backend name is permitted against the
// dictionary of restricted words
func ValidateBackendName(name string) error {
	if restrictedOriginNames.Contains(name) {
		return NewErrInvalidBackendName(name)
	}
	return nil
}

// ValidateConfigMappings ensures that named config mappings from within origin configs
// (e.g., backends.cache_name) are valid
func (l Lookup) ValidateConfigMappings(c co.Lookup, ncl negative.Lookups,
	rul ro.Lookup, rwl rwopts.Lookup, a autho.Lookup, tr tro.Lookup,
) error {
	for _, o := range l {
		if err := ValidateBackendName(o.Name); err != nil {
			return err
		}
		var ok bool
		if o.AuthenticatorName != "" {
			if o.AuthOptions, ok = a[o.AuthenticatorName]; !ok {
				return NewErrInvalidAuthenticatorName(o.AuthenticatorName, o.Name)
			}
		}
		if o.ReqRewriterName != "" {
			if _, ok = rwl[o.ReqRewriterName]; !ok {
				return NewErrInvalidRewriterName(o.ReqRewriterName, o.Name)
			}
		}
		if o.TracingConfigName != "" {
			if _, ok = tr[o.TracingConfigName]; !ok {
				return NewErrInvalidTracingName(o.TracingConfigName, o.Name)
			}
		}
		for _, p := range o.Paths {
			if p.AuthenticatorName != "none" && p.AuthenticatorName != "" {
				if p.AuthOptions, ok = a[p.AuthenticatorName]; !ok {
					return NewErrInvalidAuthenticatorName(p.AuthenticatorName,
						o.Name+"/"+p.Path)
				}
			}
			if p.ReqRewriterName != "" {
				if _, ok = rwl[p.ReqRewriterName]; !ok {
					return NewErrInvalidRewriterName(p.ReqRewriterName,
						o.Name+"/"+p.Path)
				}
			}
		}
		// ensure negative_cache_name values map to a defined Negative Cache
		if o.NegativeCacheName != "" {
			if len(ncl) == 0 {
				return NewErrInvalidNegativeCacheName(o.NegativeCacheName)
			}
			nc, ok := ncl[o.NegativeCacheName]
			if !ok || nc == nil {
				return NewErrInvalidNegativeCacheName(o.NegativeCacheName)
			}
			o.NegativeCache = nc
		}
		switch o.Provider {
		case providers.Rule:
			// Rule Type Validations
			r, ok := rul[o.RuleName]
			if !ok {
				return NewErrInvalidRuleName(o.RuleName, o.Name)
			}
			o.RuleOptions = r
		case providers.ALB:
			if o.ALBOptions == nil {
				return ao.NewErrInvalidALBOptions(o.Name)
			}
			if err := o.ALBOptions.ValidatePool(o.Name, l.Keys()); err != nil {
				return err
			}
		default:
			// No specific validation needed for other provider types
		}

		if !providers.NonCacheBackends().Contains(o.Provider) {
			if _, ok := c[o.CacheName]; !ok {
				return NewErrInvalidCacheName(o.CacheName, o.Name)
			}
		}
	}
	return nil
}

// ValidateTLSConfigs iterates the map and validates any Options that use TLS
func (l Lookup) ValidateTLSConfigs() (bool, error) {
	var serveTLS bool
	for _, o := range l {
		if o.TLS != nil {
			if err := o.TLS.Initialize(""); err != nil {
				return false, err
			}
			b, err := o.TLS.Validate()
			if err != nil {
				return false, err
			}
			if b {
				serveTLS = true
			}
		}
	}
	return serveTLS, nil
}

func (l Lookup) Keys() sets.Set[string] {
	out := sets.NewStringSet()
	for k := range l {
		out.Set(k)
	}
	return out
}

func (l Lookup) Initialize() error {
	ncb := providers.NonCacheBackends()
	for k, v := range l {
		v.Name = k
		if !ncb.Contains(v.Provider) && v.CacheName == "" {
			v.CacheName = DefaultBackendCacheName
		}
		if err := v.Initialize(k); err != nil {
			return err
		}
		if len(v.Paths) > 0 {
			err := v.Paths.Initialize()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Initialize sets up the backend Options with default values and overlays
// any values that were set during YAML unmarshaling
func (o *Options) Initialize(name string) error {
	o.Name = name

	if o.OriginURL != "" {
		parsedURL, err := url.Parse(o.OriginURL)
		if err != nil {
			return err
		}
		parsedURL.Path = strings.TrimSuffix(parsedURL.Path, "/")
		o.Scheme = parsedURL.Scheme
		o.Host = parsedURL.Host
		o.PathPrefix = parsedURL.Path
	}
	if o.CacheKeyPrefix == "" {
		o.CacheKeyPrefix = o.Host
	}
	o.TimeseriesRetention = time.Duration(o.TimeseriesRetentionFactor)
	o.DoesShard = o.MaxShardSizePoints > 0 || o.MaxShardSizeTime > 0 || o.ShardStep > 0
	if o.ShardStep > 0 && o.MaxShardSizeTime == 0 {
		o.MaxShardSizeTime = o.ShardStep
	}
	if o.CompressibleTypeList != nil {
		o.CompressibleTypes = sets.NewStringSet()
		o.CompressibleTypes.SetAll(o.CompressibleTypeList)
	}
	// enforce MaxTTL
	if o.TimeseriesTTL > o.MaxTTL {
		o.TimeseriesTTL = o.MaxTTL
	}
	if o.FastForwardTTL > o.MaxTTL {
		o.FastForwardTTL = o.MaxTTL
	}
	if o.TimeseriesEvictionMethodName != "" {
		o.TimeseriesEvictionMethodName = strings.ToLower(o.TimeseriesEvictionMethodName)
		if p, ok := evictionmethods.Names[o.TimeseriesEvictionMethodName]; ok {
			o.TimeseriesEvictionMethod = p
		}
	}
	if o.Provider == providers.ALB {
		if o.ALBOptions != nil {
			if err := o.ALBOptions.Initialize(""); err != nil {
				return err
			}
		}
	}

	if o.HealthCheck != nil {
		if err := o.HealthCheck.Initialize(""); err != nil {
			return err
		}
	}
	if o.TLS != nil {
		if err := o.TLS.Initialize(""); err != nil {
			return err
		}
	}
	return nil
}

// CloneYAMLSafe returns a copy of the Options that is safe to export to YAML without
// exposing credentials (by masking known credential fields with "*****")
func (o *Options) CloneYAMLSafe() *Options {
	co := o.Clone()
	for _, w := range co.Paths {
		w.Handler = nil
		w.KeyHasher = nil
		headers.HideAuthorizationCredentials(w.RequestHeaders)
		headers.HideAuthorizationCredentials(w.ResponseHeaders)
	}
	if co.HealthCheck != nil {
		// also strip out potentially sensitive headers
		headers.HideAuthorizationCredentials(co.HealthCheck.Headers)
	}
	return co
}

// ToYAML prints the Options as a YAML representation
func (o *Options) ToYAML() string {
	co := o.CloneYAMLSafe()
	b, _ := yaml.Marshal(co)
	return string(b)
}

func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
