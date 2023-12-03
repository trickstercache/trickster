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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/common/sigv4"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/util/copiers"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

var restrictedOriginNames = map[string]interface{}{"": true, "frontend": true}

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
	// TimeoutMS defines how long the HTTP request will wait for a response before timing out
	TimeoutMS int64 `yaml:"timeout_ms,omitempty"`
	// KeepAliveTimeoutMS defines how long an open keep-alive HTTP connection remains idle before closing
	KeepAliveTimeoutMS int64 `yaml:"keep_alive_timeout_ms,omitempty"`
	// MaxIdleConns defines maximum number of open keep-alive connections to maintain
	MaxIdleConns int `yaml:"max_idle_conns,omitempty"`
	// CacheName provides the name of the configured cache where the backend client will store it's cache data
	CacheName string `yaml:"cache_name,omitempty"`
	// CacheKeyPrefix defines the cache key prefix the backend will use when writing objects to the cache
	CacheKeyPrefix string `yaml:"cache_key_prefix,omitempty"`
	// HealthCheck is the health check options reference for this backend
	HealthCheck *ho.Options `yaml:"healthcheck,omitempty"`
	// Object Proxy Cache and Delta Proxy Cache Configurations
	// TimeseriesRetentionFactor limits the maximum the number of chronological
	// timestamps worth of data to store in cache for each query
	TimeseriesRetentionFactor int `yaml:"timeseries_retention_factor,omitempty"`
	// TimeseriesEvictionMethodName specifies which methodology ("oldest", "lru") is used to identify
	//timeseries to evict from a full cache object
	TimeseriesEvictionMethodName string `yaml:"timeseries_eviction_method,omitempty"`
	// BackfillToleranceMS prevents values with timestamps newer than the provided number of
	// milliseconds from being cached. this allows propagation of upstream backfill operations
	// that modify recently-cached data
	BackfillToleranceMS int64 `yaml:"backfill_tolerance_ms,omitempty"`
	// BackfillTolerancePoints is similar to the MS version, except that it's final value is dependent
	// on the query step value to determine the relative duration of backfill tolerance per-query
	// When both are set, the higher of the two values is used
	BackfillTolerancePoints int `yaml:"backfill_tolerance_points,omitempty"`
	// PathList is a list of Path Options that control the behavior of the given paths when requested
	Paths map[string]*po.Options `yaml:"paths,omitempty"`
	// NegativeCacheName provides the name of the Negative Cache Config to be used by this Backend
	NegativeCacheName string `yaml:"negative_cache_name,omitempty"`
	// TimeseriesTTLMS specifies the cache TTL of timeseries objects
	TimeseriesTTLMS int `yaml:"timeseries_ttl_ms,omitempty"`
	// TimeseriesTTLMS specifies the cache TTL of fast forward data
	FastForwardTTLMS int `yaml:"fastforward_ttl_ms,omitempty"`
	// MaxTTLMS specifies the maximum allowed TTL for any cache object
	MaxTTLMS int `yaml:"max_ttl_ms,omitempty"`
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
	// MaxShardSizeMS defines the max size of a timeseries request in milliseconds,
	// before sharding into multiple requests of this denomination and reconsitituting the results.
	// If MaxShardSizePoints and MaxShardSizeMS are both > 0, the configuration is invalid
	MaxShardSizeMS int `yaml:"shard_max_size_ms,omitempty"`
	// ShardStepMS defines the epoch-aligned cadence to use when creating shards. When set to 0,
	// shards are not aligned to the epoch at a specific step. MaxShardSizeMS must be perfectly
	// divisible by ShardStepMS when both are > 0, or the configuration is invalid
	ShardStepMS int `yaml:"shard_step_ms,omitempty"`

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

	// Simulated Latency
	// When LatencyMinMS > 0 and LatencyMaxMS < LatencyMinMS (e.g., 0), then LatencyMinMS of latency
	// are applied to the request. When LatencyMaxMS > LatencyMinMS, then a random amount of
	// latency between the two values will be applied to the request
	//
	// LatencyMin is the minimum amount of simulated latency to apply to each incoming request
	LatencyMinMS int `yaml:"latency_min_ms"`
	// LatencyMax is the maximum amount of simulated latency to apply to each incoming request
	LatencyMaxMS int `yaml:"latency_max_ms"`

	// Synthesized Configurations
	// These configurations are parsed versions of those defined above, and are what Trickster uses internally
	//
	// Name is the Name of the backend, taken from the Key in the Backends map[string]*BackendOptions
	Name string `yaml:"-"`
	// Router is a router.Router containing this backend's Path Routes; it is set during route registration
	Router router.Router `yaml:"-"`
	// Timeout is the time.Duration representation of TimeoutMS
	Timeout time.Duration `yaml:"-"`
	// BackfillTolerance is the time.Duration representation of BackfillToleranceMS
	BackfillTolerance time.Duration `yaml:"-"`
	// ValueRetention is the time.Duration representation of ValueRetentionSecs
	ValueRetention time.Duration `yaml:"-"`
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
	// TimeseriesTTL is the parsed value of TimeseriesTTLMS
	TimeseriesTTL time.Duration `yaml:"-"`
	// FastForwardTTL is the parsed value of FastForwardTTL
	FastForwardTTL time.Duration `yaml:"-"`
	// FastForwardPath is the paths.Options to use for upstream Fast Forward Requests
	FastForwardPath *po.Options `yaml:"-"`
	// MaxTTL is the parsed value of MaxTTLMS
	MaxTTL time.Duration `yaml:"-"`
	// HTTPClient is the Client used by Trickster to communicate with the origin
	HTTPClient *http.Client `yaml:"-"`
	// CompressibleTypes is the map version of CompressibleTypeList for fast lookup
	CompressibleTypes map[string]interface{} `yaml:"-"`
	// RuleOptions is the reference to the Rule Options as indicated by RuleName
	RuleOptions *ro.Options `yaml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions
	// DoesShard is true when sharding will be used with this origin, based on how the
	// sharding options have been configured
	DoesShard bool `yaml:"-"`
	// MaxShardSize is the parsed version of MaxShardSizeMS
	MaxShardSize time.Duration `yaml:"-"`
	// ShardStep is the parsed version of ShardStepMS
	ShardStep time.Duration `yaml:"-"`
	// SigV4
	SigV4 *sigv4.SigV4Config `yaml:"sigv4,omitempty"`
	//
	md yamlx.KeyLookup `yaml:"-"`
}

// New will return a pointer to a Backend Options with the default configuration settings
func New() *Options {
	return &Options{
		BackfillTolerance:            time.Duration(DefaultBackfillToleranceMS) * time.Millisecond,
		BackfillToleranceMS:          DefaultBackfillToleranceMS,
		BackfillTolerancePoints:      DefaultBackfillTolerancePoints,
		CacheKeyPrefix:               "",
		CacheName:                    DefaultBackendCacheName,
		CompressibleTypeList:         DefaultCompressibleTypes(),
		FastForwardTTL:               DefaultFastForwardTTLMS * time.Millisecond,
		FastForwardTTLMS:             DefaultFastForwardTTLMS,
		ForwardedHeaders:             DefaultForwardedHeaders,
		HealthCheck:                  ho.New(),
		KeepAliveTimeoutMS:           DefaultKeepAliveTimeoutMS,
		MaxIdleConns:                 DefaultMaxIdleConns,
		MaxObjectSizeBytes:           DefaultMaxObjectSizeBytes,
		MaxTTL:                       DefaultMaxTTLMS * time.Millisecond,
		MaxTTLMS:                     DefaultMaxTTLMS,
		NegativeCache:                make(map[int]time.Duration),
		NegativeCacheName:            DefaultBackendNegativeCacheName,
		Paths:                        make(map[string]*po.Options),
		RevalidationFactor:           DefaultRevalidationFactor,
		MaxShardSizePoints:           DefaultTimeseriesShardSize,
		MaxShardSizeMS:               DefaultTimeseriesShardSize,
		MaxShardSize:                 time.Duration(DefaultTimeseriesShardSize) * time.Millisecond,
		ShardStepMS:                  DefaultTimeseriesShardStep,
		ShardStep:                    time.Duration(DefaultTimeseriesShardStep) * time.Millisecond,
		TLS:                          &to.Options{},
		Timeout:                      time.Millisecond * DefaultBackendTimeoutMS,
		TimeoutMS:                    DefaultBackendTimeoutMS,
		TimeseriesEvictionMethod:     DefaultBackendTEM,
		TimeseriesEvictionMethodName: DefaultBackendTEMName,
		TimeseriesRetention:          DefaultBackendTRF,
		TimeseriesRetentionFactor:    DefaultBackendTRF,
		TimeseriesTTL:                DefaultTimeseriesTTLMS * time.Millisecond,
		TimeseriesTTLMS:              DefaultTimeseriesTTLMS,
		TracingConfigName:            DefaultTracingConfigName,
	}
}

// Clone returns an exact copy of an *backends.Options
func (o *Options) Clone() *Options {

	no := &Options{}
	no.DearticulateUpstreamRanges = o.DearticulateUpstreamRanges
	no.BackfillTolerance = o.BackfillTolerance
	no.BackfillToleranceMS = o.BackfillToleranceMS
	no.BackfillTolerancePoints = o.BackfillTolerancePoints
	no.CacheName = o.CacheName
	no.CacheKeyPrefix = o.CacheKeyPrefix
	no.DoesShard = o.DoesShard
	no.FastForwardDisable = o.FastForwardDisable
	no.FastForwardTTL = o.FastForwardTTL
	no.FastForwardTTLMS = o.FastForwardTTLMS
	no.ForwardedHeaders = o.ForwardedHeaders
	no.Host = o.Host
	no.LatencyMinMS = o.LatencyMinMS
	no.LatencyMaxMS = o.LatencyMaxMS
	no.Name = o.Name
	no.IsDefault = o.IsDefault
	no.KeepAliveTimeoutMS = o.KeepAliveTimeoutMS
	no.MaxIdleConns = o.MaxIdleConns
	no.MaxTTLMS = o.MaxTTLMS
	no.MaxTTL = o.MaxTTL
	no.MaxObjectSizeBytes = o.MaxObjectSizeBytes
	no.MultipartRangesDisabled = o.MultipartRangesDisabled
	no.Provider = o.Provider
	no.OriginURL = o.OriginURL
	no.PathPrefix = o.PathPrefix
	no.ReqRewriterName = o.ReqRewriterName
	no.RevalidationFactor = o.RevalidationFactor
	no.RuleName = o.RuleName
	no.Scheme = o.Scheme
	no.MaxShardSize = o.MaxShardSize
	no.MaxShardSizeMS = o.MaxShardSizeMS
	no.MaxShardSizePoints = o.MaxShardSizePoints
	no.ShardStep = o.ShardStep
	no.ShardStepMS = o.ShardStepMS
	no.Timeout = o.Timeout
	no.TimeoutMS = o.TimeoutMS
	no.TimeseriesRetention = o.TimeseriesRetention
	no.TimeseriesRetentionFactor = o.TimeseriesRetentionFactor
	no.TimeseriesEvictionMethodName = o.TimeseriesEvictionMethodName
	no.TimeseriesEvictionMethod = o.TimeseriesEvictionMethod
	no.TimeseriesTTL = o.TimeseriesTTL
	no.TimeseriesTTLMS = o.TimeseriesTTLMS
	no.ValueRetention = o.ValueRetention

	no.TracingConfigName = o.TracingConfigName

	if o.HealthCheck != nil {
		no.HealthCheck = o.HealthCheck.Clone()
	}

	no.Hosts = copiers.CopyStrings(o.Hosts)
	no.CompressibleTypeList = copiers.CopyStrings(no.CompressibleTypeList)

	if o.CompressibleTypes != nil {
		no.CompressibleTypes = make(map[string]interface{})
		for k := range o.CompressibleTypes {
			no.CompressibleTypes[k] = true
		}
	}

	no.Paths = make(map[string]*po.Options)
	for l, p := range o.Paths {
		no.Paths[l] = p.Clone()
	}

	no.NegativeCacheName = o.NegativeCacheName
	if o.NegativeCache != nil {
		m := make(map[int]time.Duration)
		for c, t := range o.NegativeCache {
			m[c] = t
		}
		no.NegativeCache = m
	}

	if o.TLS != nil {
		no.TLS = o.TLS.Clone()
	}
	no.RequireTLS = o.RequireTLS

	if o.FastForwardPath != nil {
		no.FastForwardPath = o.FastForwardPath.Clone()
	}

	if o.RuleOptions != nil {
		no.RuleOptions = o.RuleOptions.Clone()
	}

	if o.ALBOptions != nil {
		no.ALBOptions = o.ALBOptions.Clone()
	}

	if o.Prometheus != nil {
		no.Prometheus = o.Prometheus.Clone()
	}

	return no
}

// Validate validates the Lookup collection of Backend Options
func (l Lookup) Validate(ncl negative.Lookups) error {
	for k, o := range l {
		if o.Provider == "" {
			return NewErrMissingProvider(k)
		}
		if (o.Provider != "rule" && o.Provider != "alb") && o.OriginURL == "" {
			return NewErrMissingOriginURL(k)
		}
		url, err := url.Parse(o.OriginURL)
		if err != nil {
			return err
		}
		url.Path = strings.TrimSuffix(url.Path, "/")
		o.Name = k
		o.Scheme = url.Scheme
		o.Host = url.Host
		o.PathPrefix = url.Path
		o.Timeout = time.Duration(o.TimeoutMS) * time.Millisecond
		o.BackfillTolerance = time.Duration(o.BackfillToleranceMS) * time.Millisecond
		o.TimeseriesRetention = time.Duration(o.TimeseriesRetentionFactor)
		o.TimeseriesTTL = time.Duration(o.TimeseriesTTLMS) * time.Millisecond
		o.FastForwardTTL = time.Duration(o.FastForwardTTLMS) * time.Millisecond
		o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond
		o.DoesShard = o.MaxShardSizePoints > 0 || o.MaxShardSizeMS > 0 || o.ShardStepMS > 0
		o.ShardStep = time.Duration(o.ShardStepMS) * time.Millisecond
		o.MaxShardSize = time.Duration(o.MaxShardSizeMS) * time.Millisecond

		if o.MaxShardSizeMS > 0 && o.MaxShardSizePoints > 0 {
			return ErrInvalidMaxShardSize
		}

		if o.ShardStepMS > 0 && o.MaxShardSizeMS == 0 {
			o.MaxShardSize = o.ShardStep
		}

		if o.ShardStep > 0 && o.MaxShardSize%o.ShardStep != 0 {
			return ErrInvalidMaxShardSizeMS
		}

		if o.CompressibleTypeList != nil {
			o.CompressibleTypes = make(map[string]interface{})
			for _, v := range o.CompressibleTypeList {
				o.CompressibleTypes[v] = true
			}
		}
		if o.CacheKeyPrefix == "" {
			o.CacheKeyPrefix = o.Host
		}

		if ncl != nil {
			nc := ncl.Get(o.NegativeCacheName)
			if nc == nil {
				return NewErrInvalidNegativeCacheName(o.NegativeCacheName)
			}
			o.NegativeCache = nc
		}

		// enforce MaxTTL
		if o.TimeseriesTTLMS > o.MaxTTLMS {
			o.TimeseriesTTLMS = o.MaxTTLMS
			o.TimeseriesTTL = o.MaxTTL
		}

		// unlikely but why not spend a few nanoseconds to check it at startup
		if o.FastForwardTTLMS > o.MaxTTLMS {
			o.FastForwardTTLMS = o.MaxTTLMS
			o.FastForwardTTL = o.MaxTTL
		}
	}
	return nil
}

// ValidateBackendName ensures the backend name is permitted against the dictionary of
// restricted words
func ValidateBackendName(name string) error {
	if _, ok := restrictedOriginNames[name]; ok {
		return NewErrInvalidBackendName(name)
	}
	return nil
}

// ValidateConfigMappings ensures that named config mappings from within origin configs
// (e.g., backends.cache_name) are valid
func (l Lookup) ValidateConfigMappings(rules ro.Lookup, caches co.Lookup) error {
	for _, o := range l {
		if err := ValidateBackendName(o.Name); err != nil {
			return err
		}
		switch o.Provider {
		case "rule":
			// Rule Type Validations
			r, ok := rules[o.RuleName]
			if !ok {
				return NewErrInvalidRuleName(o.RuleName, o.Name)
			}
			r.Name = o.RuleName
			o.RuleOptions = r
		case "alb":
			// ALB Validations
			if ao := o.ALBOptions; ao != nil {
				for _, bn := range ao.Pool {
					if _, ok := l[bn]; !ok {
						return NewErrInvalidALBOptions(bn, o.Name)
					}
				}
			}
		default:
			if _, ok := caches[o.CacheName]; !ok {
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

// SetDefaults iterates a YAML Config
func SetDefaults(
	name string,
	o *Options,
	metadata yamlx.KeyLookup,
	crw map[string]rewriter.RewriteInstructions,
	backends Lookup,
	activeCaches map[string]interface{},
) (*Options, error) {

	if metadata == nil {
		return nil, ErrInvalidMetadata
	}

	no := New()
	no.Name = name

	if metadata.IsDefined("backends", name, "req_rewriter_name") && o.ReqRewriterName != "" {
		no.ReqRewriterName = o.ReqRewriterName
		ri, ok := crw[no.ReqRewriterName]
		if !ok {
			return nil, NewErrInvalidRewriterName(no.ReqRewriterName, name)
		}
		no.ReqRewriter = ri
	}

	if metadata.IsDefined("backends", name, "provider") {
		no.Provider = o.Provider
	}

	if metadata.IsDefined("backends", name, "rule_name") {
		no.RuleName = o.RuleName
	}

	if metadata.IsDefined("backends", name, "path_routing_disabled") {
		no.PathRoutingDisabled = o.PathRoutingDisabled
	}

	if metadata.IsDefined("backends", name, "hosts") && o != nil {
		no.Hosts = copiers.CopyStrings(o.Hosts)
	}

	if metadata.IsDefined("backends", name, "is_default") {
		no.IsDefault = o.IsDefault
	}
	// If there is only one backend and is_default is not explicitly false, make it true
	if len(backends) == 1 && (!metadata.IsDefined("backends", name, "is_default")) {
		no.IsDefault = true
	}

	if metadata.IsDefined("backends", name, "forwarded_headers") {
		no.ForwardedHeaders = o.ForwardedHeaders
	}

	if metadata.IsDefined("backends", name, "require_tls") {
		no.RequireTLS = o.RequireTLS
	}

	if metadata.IsDefined("backends", name, "cache_name") {
		no.CacheName = o.CacheName
	}
	activeCaches[no.CacheName] = true

	if metadata.IsDefined("backends", name, "cache_key_prefix") {
		no.CacheKeyPrefix = o.CacheKeyPrefix
	}

	if metadata.IsDefined("backends", name, "origin_url") {
		no.OriginURL = o.OriginURL
	}

	if metadata.IsDefined("backends", name, "compressible_types") {
		no.CompressibleTypeList = o.CompressibleTypeList
	}

	if metadata.IsDefined("backends", name, "timeout_ms") {
		no.TimeoutMS = o.TimeoutMS
	}

	if metadata.IsDefined("backends", name, "max_idle_conns") {
		no.MaxIdleConns = o.MaxIdleConns
	}

	if metadata.IsDefined("backends", name, "keep_alive_timeout_ms") {
		no.KeepAliveTimeoutMS = o.KeepAliveTimeoutMS
	}

	if metadata.IsDefined("backends", name, "shard_max_size_points") {
		no.MaxShardSizePoints = o.MaxShardSizePoints
	}

	if metadata.IsDefined("backends", name, "shard_max_size_ms") {
		no.MaxShardSizeMS = o.MaxShardSizeMS
	}

	if metadata.IsDefined("backends", name, "shard_step_ms") {
		no.ShardStepMS = o.ShardStepMS
	}

	if metadata.IsDefined("backends", name, "timeseries_retention_factor") {
		no.TimeseriesRetentionFactor = o.TimeseriesRetentionFactor
	}

	if metadata.IsDefined("backends", name, "timeseries_eviction_method") {
		no.TimeseriesEvictionMethodName = strings.ToLower(o.TimeseriesEvictionMethodName)
		if p, ok := evictionmethods.Names[no.TimeseriesEvictionMethodName]; ok {
			no.TimeseriesEvictionMethod = p
		}
	}

	if metadata.IsDefined("backends", name, "timeseries_ttl_ms") {
		no.TimeseriesTTLMS = o.TimeseriesTTLMS
	}

	if metadata.IsDefined("backends", name, "max_ttl_ms") {
		no.MaxTTLMS = o.MaxTTLMS
	}

	if metadata.IsDefined("backends", name, "fastforward_ttl_ms") {
		no.FastForwardTTLMS = o.FastForwardTTLMS
	}

	if metadata.IsDefined("backends", name, "fast_forward_disable") {
		no.FastForwardDisable = o.FastForwardDisable
	}

	if metadata.IsDefined("backends", name, "backfill_tolerance_ms") {
		no.BackfillToleranceMS = o.BackfillToleranceMS
	}

	if metadata.IsDefined("backends", name, "backfill_tolerance_points") {
		no.BackfillTolerancePoints = o.BackfillTolerancePoints
	}

	if metadata.IsDefined("backends", name, "paths") {
		err := po.SetDefaults(name, metadata, o.Paths, crw)
		if err != nil {
			return nil, err
		}
		for k, v := range o.Paths {
			no.Paths[k] = v.Clone()
		}
	}

	if metadata.IsDefined("backends", name, "alb") {
		opts, err := ao.SetDefaults(name, o.ALBOptions, metadata)
		if err != nil {
			return nil, err
		}
		no.ALBOptions = opts
	}

	if metadata.IsDefined("backends", name, "negative_cache_name") {
		no.NegativeCacheName = o.NegativeCacheName
	}

	if metadata.IsDefined("backends", name, "tracing_name") {
		no.TracingConfigName = o.TracingConfigName
	}

	if metadata.IsDefined("backends", name, "healthcheck") {
		no.HealthCheck = o.HealthCheck
		// because each backend provider has different default health check options, these
		// provided options will be overlaid onto the defaults during registration
		if no.HealthCheck != nil {
			no.HealthCheck.SetMetaData(metadata)
		}
	}

	if metadata.IsDefined("backends", name, "max_object_size_bytes") {
		no.MaxObjectSizeBytes = o.MaxObjectSizeBytes
	}

	if metadata.IsDefined("backends", name, "revalidation_factor") {
		no.RevalidationFactor = o.RevalidationFactor
	}

	if metadata.IsDefined("backends", name, "multipart_ranges_disabled") {
		no.MultipartRangesDisabled = o.MultipartRangesDisabled
	}

	if metadata.IsDefined("backends", name, "dearticulate_upstream_ranges") {
		no.DearticulateUpstreamRanges = o.DearticulateUpstreamRanges
	}

	if metadata.IsDefined("backends", name, "tls") {
		no.TLS = &to.Options{
			InsecureSkipVerify:        o.TLS.InsecureSkipVerify,
			CertificateAuthorityPaths: o.TLS.CertificateAuthorityPaths,
			PrivateKeyPath:            o.TLS.PrivateKeyPath,
			FullChainCertPath:         o.TLS.FullChainCertPath,
			ClientCertPath:            o.TLS.ClientCertPath,
			ClientKeyPath:             o.TLS.ClientKeyPath,
		}
	}

	if metadata.IsDefined("backends", name, "prometheus") {
		no.Prometheus = o.Prometheus.Clone()
	}

	if metadata.IsDefined("backends", name, "latency_min_ms") {
		no.LatencyMinMS = o.LatencyMinMS
	}

	if metadata.IsDefined("backends", name, "latency_max_ms") {
		no.LatencyMaxMS = o.LatencyMaxMS
	}

	if metadata.IsDefined("backends", name, "sigv4") {
		no.SigV4 = o.SigV4
	}

	return no, nil
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
