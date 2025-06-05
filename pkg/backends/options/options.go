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
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/tree"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"github.com/prometheus/common/sigv4"
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
	// PathList is a list of Path Options that control the behavior of the given paths when requested
	Paths po.Lookup `yaml:"paths,omitempty"`
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

// New will return a pointer to a Backend Options with the default configuration settings
func New() *Options {
	return &Options{
		BackfillTolerance:            DefaultBackfillTolerance,
		BackfillTolerancePoints:      DefaultBackfillTolerancePoints,
		CacheKeyPrefix:               "",
		CacheName:                    DefaultBackendCacheName,
		CompressibleTypeList:         DefaultCompressibleTypes(),
		FastForwardTTL:               DefaultFastForwardTTL,
		ForwardedHeaders:             DefaultForwardedHeaders,
		HealthCheck:                  ho.New(),
		KeepAliveTimeout:             DefaultKeepAliveTimeout,
		MaxIdleConns:                 DefaultMaxIdleConns,
		MaxObjectSizeBytes:           DefaultMaxObjectSizeBytes,
		MaxTTL:                       DefaultMaxTTL,
		NegativeCache:                make(map[int]time.Duration),
		NegativeCacheName:            DefaultBackendNegativeCacheName,
		Paths:                        make(po.Lookup),
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

	no := &Options{}
	no.DearticulateUpstreamRanges = o.DearticulateUpstreamRanges
	no.BackfillTolerance = o.BackfillTolerance
	no.BackfillTolerance = o.BackfillTolerance
	no.BackfillTolerancePoints = o.BackfillTolerancePoints
	no.CacheName = o.CacheName
	no.CacheKeyPrefix = o.CacheKeyPrefix
	no.DoesShard = o.DoesShard
	no.FastForwardDisable = o.FastForwardDisable
	no.FastForwardTTL = o.FastForwardTTL
	no.ForwardedHeaders = o.ForwardedHeaders
	no.Host = o.Host
	no.LatencyMin = o.LatencyMin
	no.LatencyMax = o.LatencyMax
	no.Name = o.Name
	no.IsDefault = o.IsDefault
	no.KeepAliveTimeout = o.KeepAliveTimeout
	no.MaxIdleConns = o.MaxIdleConns
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
	no.MaxShardSizeTime = o.MaxShardSizeTime
	no.MaxShardSizePoints = o.MaxShardSizePoints
	no.ShardStep = o.ShardStep
	no.Timeout = o.Timeout
	no.TimeseriesRetention = o.TimeseriesRetention
	no.TimeseriesRetentionFactor = o.TimeseriesRetentionFactor
	no.TimeseriesEvictionMethodName = o.TimeseriesEvictionMethodName
	no.TimeseriesEvictionMethod = o.TimeseriesEvictionMethod
	no.TimeseriesTTL = o.TimeseriesTTL

	no.TracingConfigName = o.TracingConfigName

	if o.HealthCheck != nil {
		no.HealthCheck = o.HealthCheck.Clone()
	}

	no.Hosts = slices.Clone(o.Hosts)
	no.CompressibleTypeList = slices.Clone(no.CompressibleTypeList)

	if o.CompressibleTypes != nil {
		no.CompressibleTypes = maps.Clone(o.CompressibleTypes)
	}

	no.Paths = make(po.Lookup, len(o.Paths))
	for l, p := range o.Paths {
		no.Paths[l] = p.Clone()
	}

	no.NegativeCacheName = o.NegativeCacheName
	if o.NegativeCache != nil {
		no.NegativeCache = maps.Clone(o.NegativeCache)
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

	no.AuthenticatorName = o.AuthenticatorName
	if o.AuthOptions != nil {
		no.AuthOptions = o.AuthOptions.Clone()
	}

	return no
}

// Validate validates the Backend Options
func (o *Options) Validate() error {
	if err := ValidateBackendName(o.Name); err != nil {
		return err
	}
	if o.Provider == "" {
		return NewErrMissingProvider(o.Name)
	}
	if !providers.NonOriginBackends().Contains(o.Provider) && o.OriginURL == "" {
		return NewErrMissingOriginURL(o.Name)
	}
	url, err := url.Parse(o.OriginURL)
	if err != nil {
		return err
	}
	url.Path = strings.TrimSuffix(url.Path, "/")
	o.Scheme = url.Scheme
	o.Host = url.Host
	o.PathPrefix = url.Path
	o.TimeseriesRetention = time.Duration(o.TimeseriesRetentionFactor)
	o.DoesShard = o.MaxShardSizePoints > 0 || o.MaxShardSizeTime > 0 || o.ShardStep > 0

	if o.MaxShardSizeTime > 0 && o.MaxShardSizePoints > 0 {
		return ErrInvalidMaxShardSize
	}

	if o.ShardStep > 0 && o.MaxShardSizeTime == 0 {
		o.MaxShardSizeTime = o.ShardStep
	}

	if o.ShardStep > 0 && o.MaxShardSizeTime%o.ShardStep != 0 {
		return ErrInvalidMaxShardSizeTime
	}

	if len(o.Paths) > 0 {
		if err := o.Paths.Validate(); err != nil {
			return err
		}
	}

	if o.CompressibleTypeList != nil {
		o.CompressibleTypes = sets.NewStringSet()
		o.CompressibleTypes.SetAll(o.CompressibleTypeList)
	}
	if o.CacheKeyPrefix == "" {
		o.CacheKeyPrefix = o.Host
	}

	// enforce MaxTTL
	if o.TimeseriesTTL > o.MaxTTL {
		o.TimeseriesTTL = o.MaxTTL
	}

	// unlikely but why not spend a few nanoseconds to check it at startup
	if o.FastForwardTTL > o.MaxTTL {
		o.FastForwardTTL = o.MaxTTL
	}
	return nil
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
		if err := o.Validate(); err != nil {
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
	rul ro.Lookup, rwl rwopts.Lookup, a autho.Lookup, tr tro.Lookup) error {
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

// OverlayYAMLData extracts supported backend Options values from the yaml map,
// and returns a new default Options overlaid with the extracted values
func OverlayYAMLData(
	name string,
	o *Options,
	backends Lookup,
	activeCaches sets.Set[string],
	y yamlx.KeyLookup,
) (*Options, error) {

	if y == nil {
		return nil, ErrInvalidMetadata
	}

	no := New()
	no.Name = name

	if y.IsDefined("backends", name, "req_rewriter_name") && o.ReqRewriterName != "" {
		no.ReqRewriterName = o.ReqRewriterName
	}

	if y.IsDefined("backends", name, "provider") {
		no.Provider = o.Provider
	}

	if y.IsDefined("backends", name, "rule_name") {
		no.RuleName = o.RuleName
	}

	if y.IsDefined("backends", name, "path_routing_disabled") {
		no.PathRoutingDisabled = o.PathRoutingDisabled
	}

	if y.IsDefined("backends", name, "hosts") && o != nil {
		no.Hosts = slices.Clone(o.Hosts)
	}

	if y.IsDefined("backends", name, "is_default") {
		no.IsDefault = o.IsDefault
	}
	// If there is only one backend and is_default is not explicitly false, make it true
	if len(backends) == 1 && (!y.IsDefined("backends", name, "is_default")) {
		no.IsDefault = true
	}

	if y.IsDefined("backends", name, "forwarded_headers") {
		no.ForwardedHeaders = o.ForwardedHeaders
	}

	if y.IsDefined("backends", name, "require_tls") {
		no.RequireTLS = o.RequireTLS
	}

	if y.IsDefined("backends", name, "cache_name") {
		no.CacheName = o.CacheName
	}
	activeCaches.Set(no.CacheName)

	if y.IsDefined("backends", name, "cache_key_prefix") {
		no.CacheKeyPrefix = o.CacheKeyPrefix
	}

	if y.IsDefined("backends", name, "origin_url") {
		no.OriginURL = o.OriginURL
	}

	if y.IsDefined("backends", name, "compressible_types") {
		no.CompressibleTypeList = o.CompressibleTypeList
	}

	if y.IsDefined("backends", name, "timeout") {
		no.Timeout = o.Timeout
	}

	if y.IsDefined("backends", name, "max_idle_conns") {
		no.MaxIdleConns = o.MaxIdleConns
	}

	if y.IsDefined("backends", name, "keep_alive_timeout") {
		no.KeepAliveTimeout = o.KeepAliveTimeout
	}

	if y.IsDefined("backends", name, "shard_max_size_points") {
		no.MaxShardSizePoints = o.MaxShardSizePoints
	}

	if y.IsDefined("backends", name, "shard_max_size_time") {
		no.MaxShardSizeTime = o.MaxShardSizeTime
	}

	if y.IsDefined("backends", name, "shard_step") {
		no.ShardStep = o.ShardStep
	}

	if y.IsDefined("backends", name, "timeseries_retention_factor") {
		no.TimeseriesRetentionFactor = o.TimeseriesRetentionFactor
	}

	if y.IsDefined("backends", name, "timeseries_eviction_method") {
		no.TimeseriesEvictionMethodName = strings.ToLower(o.TimeseriesEvictionMethodName)
		if p, ok := evictionmethods.Names[no.TimeseriesEvictionMethodName]; ok {
			no.TimeseriesEvictionMethod = p
		}
	}

	if y.IsDefined("backends", name, "timeseries_ttl") {
		no.TimeseriesTTL = o.TimeseriesTTL
	}

	if y.IsDefined("backends", name, "max_ttl") {
		no.MaxTTL = o.MaxTTL
	}

	if y.IsDefined("backends", name, "fastforward_ttl") {
		no.FastForwardTTL = o.FastForwardTTL
	}

	if y.IsDefined("backends", name, "fast_forward_disable") {
		no.FastForwardDisable = o.FastForwardDisable
	}

	if y.IsDefined("backends", name, "backfill_tolerance") {
		no.BackfillTolerance = o.BackfillTolerance
	}

	if y.IsDefined("backends", name, "backfill_tolerance_points") {
		no.BackfillTolerancePoints = o.BackfillTolerancePoints
	}

	if y.IsDefined("backends", name, "paths") {
		paths, err := po.OverlayYAMLData(name, o.Paths, y)
		if err != nil {
			return nil, err
		}
		maps.Copy(no.Paths, paths)
	}

	if y.IsDefined("backends", name, providers.ALB) {
		opts, err := ao.OverlayYAMLData(name, o.ALBOptions, y)
		if err != nil {
			return nil, err
		}
		no.ALBOptions = opts
	}

	if y.IsDefined("backends", name, "negative_cache_name") {
		no.NegativeCacheName = o.NegativeCacheName
	}

	if y.IsDefined("backends", name, "tracing_name") {
		no.TracingConfigName = o.TracingConfigName
	}

	if y.IsDefined("backends", name, "healthcheck") {
		no.HealthCheck = o.HealthCheck
		// because each backend provider has different default health check options, these
		// provided options will be overlaid onto the defaults during registration
		if no.HealthCheck != nil {
			no.HealthCheck.SetYAMLData(y)
		}
	}

	if y.IsDefined("backends", name, "max_object_size_bytes") {
		no.MaxObjectSizeBytes = o.MaxObjectSizeBytes
	}

	if y.IsDefined("backends", name, "revalidation_factor") {
		no.RevalidationFactor = o.RevalidationFactor
	}

	if y.IsDefined("backends", name, "multipart_ranges_disabled") {
		no.MultipartRangesDisabled = o.MultipartRangesDisabled
	}

	if y.IsDefined("backends", name, "dearticulate_upstream_ranges") {
		no.DearticulateUpstreamRanges = o.DearticulateUpstreamRanges
	}

	if y.IsDefined("backends", name, "tls") {
		no.TLS = &to.Options{
			InsecureSkipVerify:        o.TLS.InsecureSkipVerify,
			CertificateAuthorityPaths: o.TLS.CertificateAuthorityPaths,
			PrivateKeyPath:            o.TLS.PrivateKeyPath,
			FullChainCertPath:         o.TLS.FullChainCertPath,
			ClientCertPath:            o.TLS.ClientCertPath,
			ClientKeyPath:             o.TLS.ClientKeyPath,
		}
	}

	if y.IsDefined("backends", name, providers.Prometheus) {
		no.Prometheus = o.Prometheus.Clone()
	}

	if y.IsDefined("backends", name, "latency_min") {
		no.LatencyMin = o.LatencyMin
	}

	if y.IsDefined("backends", name, "latency_max") {
		no.LatencyMax = o.LatencyMax
	}

	if y.IsDefined("backends", name, "sigv4") {
		no.SigV4 = o.SigV4
	}

	if y.IsDefined("backends", name, "authenticator_name") {
		no.AuthenticatorName = o.AuthenticatorName
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
