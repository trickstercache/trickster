/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	ro "github.com/tricksterproxy/trickster/pkg/backends/rule/options"
	"github.com/tricksterproxy/trickster/pkg/cache/evictionmethods"
	"github.com/tricksterproxy/trickster/pkg/cache/negative"
	co "github.com/tricksterproxy/trickster/pkg/cache/options"
	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter"
	to "github.com/tricksterproxy/trickster/pkg/proxy/tls/options"

	"github.com/gorilla/mux"
)

var restrictedOriginNames = map[string]bool{"frontend": true}

// Lookup is a map of Options
type Lookup map[string]*Options

// Options is a collection of configurations for Trickster backends
type Options struct {

	// HTTP and Proxy Configurations
	//
	// Hosts identifies the frontend hostnames this backend should handle (virtual hosting)
	Hosts []string `toml:"hosts"`
	// Provider describes the type of backend (e.g., 'prometheus')
	Provider string `toml:"provider"`
	// OriginURL provides the base upstream URL for all proxied requests to this Backend.
	// it can be as simple as http://example.com or as complex as https://example.com:8443/path/prefix
	OriginURL string `toml:"origin_url"`
	// TimeoutMS defines how long the HTTP request will wait for a response before timing out
	TimeoutMS int64 `toml:"timeout_ms"`
	// KeepAliveTimeoutMS defines how long an open keep-alive HTTP connection remains idle before closing
	KeepAliveTimeoutMS int64 `toml:"keep_alive_timeout_ms"`
	// MaxIdleConns defines maximum number of open keep-alive connections to maintain
	MaxIdleConns int `toml:"max_idle_conns"`
	// CacheName provides the name of the configured cache where the backend client will store it's cache data
	CacheName string `toml:"cache_name"`
	// CacheKeyPrefix defines the cache key prefix the backend will use when writing objects to the cache
	CacheKeyPrefix string `toml:"cache_key_prefix"`
	// HealthCheckUpstreamPath provides the URL path for the upstream health check
	HealthCheckUpstreamPath string `toml:"health_check_upstream_path"`
	// HealthCheckVerb provides the HTTP verb to use when making an upstream health check
	HealthCheckVerb string `toml:"health_check_verb"`
	// HealthCheckQuery provides the HTTP query parameters to use when making an upstream health check
	HealthCheckQuery string `toml:"health_check_query"`
	// HealthCheckHeaders provides the HTTP Headers to apply when making an upstream health check
	HealthCheckHeaders map[string]string `toml:"health_check_headers"`
	// Object Proxy Cache and Delta Proxy Cache Configurations
	// TimeseriesRetentionFactor limits the maximum the number of chronological
	// timestamps worth of data to store in cache for each query
	TimeseriesRetentionFactor int `toml:"timeseries_retention_factor"`
	// TimeseriesEvictionMethodName specifies which methodology ("oldest", "lru") is used to identify
	//timeseries to evict from a full cache object
	TimeseriesEvictionMethodName string `toml:"timeseries_eviction_method"`
	// BackfillToleranceMS prevents values with timestamps newer than the provided
	// number of seconds from being cached this allows propagation of upstream backfill operations
	// that modify recently-served data
	BackfillToleranceMS int64 `toml:"backfill_tolerance_ms"`
	// PathList is a list of Path Options that control the behavior of the given paths when requested
	Paths map[string]*po.Options `toml:"paths"`
	// NegativeCacheName provides the name of the Negative Cache Config to be used by this Backend
	NegativeCacheName string `toml:"negative_cache_name"`
	// TimeseriesTTLMS specifies the cache TTL of timeseries objects
	TimeseriesTTLMS int `toml:"timeseries_ttl_ms"`
	// TimeseriesTTLMS specifies the cache TTL of fast forward data
	FastForwardTTLMS int `toml:"fastforward_ttl_ms"`
	// MaxTTLMS specifies the maximum allowed TTL for any cache object
	MaxTTLMS int `toml:"max_ttl_ms"`
	// RevalidationFactor specifies how many times to multiply the object freshness lifetime
	// by to calculate an absolute cache TTL
	RevalidationFactor float64 `toml:"revalidation_factor"`
	// MaxObjectSizeBytes specifies the max objectsize to be accepted for any given cache object
	MaxObjectSizeBytes int `toml:"max_object_size_bytes"`
	// CompressableTypeList specifies the HTTP Object Content Types that will be compressed internally
	// when stored in the Trickster cache
	CompressableTypeList []string `toml:"compressable_types"`
	// TracingConfigName provides the name of the Tracing Config to be used by this Backend
	TracingConfigName string `toml:"tracing_name"`
	// RuleName provides the name of the rule config to be used by this backend.
	// This is only effective if the Backend provider is 'rule'
	RuleName string `toml:"rule_name"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request prior to
	// processing by the backend client
	ReqRewriterName string `toml:"req_rewriter_name"`

	// TLS is the TLS Configuration for the Frontend and Backend
	TLS *to.Options `toml:"tls"`

	// ForwardedHeaders indicates the class of 'Forwarded' header to attach to upstream requests
	ForwardedHeaders string `toml:"forwarded_headers"`

	// IsDefault indicates if this is the d.Default backend for any request not matching a configured route
	IsDefault bool `toml:"is_default"`
	// FastForwardDisable indicates whether the FastForward feature should be disabled for this backend
	FastForwardDisable bool `toml:"fast_forward_disable"`
	// PathRoutingDisabled, when true, will bypass /backendName/path route registrations
	PathRoutingDisabled bool `toml:"path_routing_disabled"`
	// RequireTLS, when true, indicates this Backend Config's paths must only be registered with the TLS Router
	RequireTLS bool `toml:"require_tls"`
	// MultipartRangesDisabled, when true, indicates that if a downstream client requests multiple ranges
	// in a single request, Trickster will instead request and return a 200 OK with the full object body
	MultipartRangesDisabled bool `toml:"multipart_ranges_disabled"`
	// DearticulateUpstreamRanges, when true, indicates that when Trickster requests multiple ranges from
	// the backend, that they be requested as individual upstream requests instead of a single request that
	// expects a multipart response	// this optimizes Trickster to request as few bytes as possible when
	// fronting backends that only support single range requests
	DearticulateUpstreamRanges bool `toml:"dearticulate_upstream_ranges"`

	// Synthesized Configurations
	// These configurations are parsed versions of those defined above, and are what Trickster uses internally
	//
	// Name is the Name of the backend, taken from the Key in the Backends map[string]*BackendOptions
	Name string `toml:"-"`
	// Router is a mux.Router containing this backend's Path Routes; it is set during route registration
	Router *mux.Router `toml:"-"`
	// Timeout is the time.Duration representation of TimeoutMS
	Timeout time.Duration `toml:"-"`
	// BackfillTolerance is the time.Duration representation of BackfillToleranceMS
	BackfillTolerance time.Duration `toml:"-"`
	// ValueRetention is the time.Duration representation of ValueRetentionSecs
	ValueRetention time.Duration `toml:"-"`
	// Scheme is the layer 7 protocol indicator (e.g. 'http'), derived from OriginURL
	Scheme string `toml:"-"`
	// Host is the upstream hostname/IP[:port] the backend client will connect to when fetching uncached data,
	// derived from OriginURL
	Host string `toml:"-"`
	// PathPrefix provides any prefix added to the front of the requested path when constructing the upstream
	// request url, derived from OriginURL
	PathPrefix string `toml:"-"`
	// NegativeCache provides a map for the negative cache, with TTLs converted to time.Durations
	NegativeCache negative.Lookup `toml:"-"`
	// TimeseriesRetention when subtracted from time.Now() represents the oldest allowable timestamp in a
	// timeseries when EvictionMethod is 'oldest'
	TimeseriesRetention time.Duration `toml:"-"`
	// TimeseriesEvictionMethod is the parsed value of TimeseriesEvictionMethodName
	TimeseriesEvictionMethod evictionmethods.TimeseriesEvictionMethod `toml:"-"`
	// TimeseriesTTL is the parsed value of TimeseriesTTLMS
	TimeseriesTTL time.Duration `toml:"-"`
	// FastForwardTTL is the parsed value of FastForwardTTL
	FastForwardTTL time.Duration `toml:"-"`
	// FastForwardPath is the paths.Options to use for upstream Fast Forward Requests
	FastForwardPath *po.Options `toml:"-"`
	// MaxTTL is the parsed value of MaxTTLMS
	MaxTTL time.Duration `toml:"-"`
	// HTTPClient is the Client used by Trickster to communicate with the origin
	HTTPClient *http.Client `toml:"-"`
	// CompressableTypes is the map version of CompressableTypeList for fast lookup
	CompressableTypes map[string]bool `toml:"-"`
	// RuleOptions is the reference to the Rule Options as indicated by RuleName
	RuleOptions *ro.Options `toml:"-"`
	// ReqRewriter is the rewriter handler as indicated by RuleName
	ReqRewriter rewriter.RewriteInstructions
}

// New will return a pointer to an BackendOptions with the default configuration settings
func New() *Options {
	return &Options{
		BackfillTolerance:            d.DefaultBackfillToleranceMS,
		BackfillToleranceMS:          d.DefaultBackfillToleranceMS,
		CacheKeyPrefix:               "",
		CacheName:                    d.DefaultBackendCacheName,
		CompressableTypeList:         d.DefaultCompressableTypes(),
		FastForwardTTL:               d.DefaultFastForwardTTLMS * time.Millisecond,
		FastForwardTTLMS:             d.DefaultFastForwardTTLMS,
		ForwardedHeaders:             d.DefaultForwardedHeaders,
		HealthCheckHeaders:           make(map[string]string),
		HealthCheckQuery:             d.DefaultHealthCheckQuery,
		HealthCheckUpstreamPath:      d.DefaultHealthCheckPath,
		HealthCheckVerb:              d.DefaultHealthCheckVerb,
		KeepAliveTimeoutMS:           d.DefaultKeepAliveTimeoutMS,
		MaxIdleConns:                 d.DefaultMaxIdleConns,
		MaxObjectSizeBytes:           d.DefaultMaxObjectSizeBytes,
		MaxTTL:                       d.DefaultMaxTTLMS * time.Millisecond,
		MaxTTLMS:                     d.DefaultMaxTTLMS,
		NegativeCache:                make(map[int]time.Duration),
		NegativeCacheName:            d.DefaultBackendNegativeCacheName,
		Paths:                        make(map[string]*po.Options),
		RevalidationFactor:           d.DefaultRevalidationFactor,
		TLS:                          &to.Options{},
		Timeout:                      time.Millisecond * d.DefaultBackendTimeoutMS,
		TimeoutMS:                    d.DefaultBackendTimeoutMS,
		TimeseriesEvictionMethod:     d.DefaultBackendTEM,
		TimeseriesEvictionMethodName: d.DefaultBackendTEMName,
		TimeseriesRetention:          d.DefaultBackendTRF,
		TimeseriesRetentionFactor:    d.DefaultBackendTRF,
		TimeseriesTTL:                d.DefaultTimeseriesTTLMS * time.Millisecond,
		TimeseriesTTLMS:              d.DefaultTimeseriesTTLMS,
		TracingConfigName:            d.DefaultTracingConfigName,
	}
}

// Clone returns an exact copy of an *backends.Options
func (oc *Options) Clone() *Options {

	o := &Options{}
	o.DearticulateUpstreamRanges = oc.DearticulateUpstreamRanges
	o.BackfillTolerance = oc.BackfillTolerance
	o.BackfillToleranceMS = oc.BackfillToleranceMS
	o.CacheName = oc.CacheName
	o.CacheKeyPrefix = oc.CacheKeyPrefix
	o.FastForwardDisable = oc.FastForwardDisable
	o.FastForwardTTL = oc.FastForwardTTL
	o.FastForwardTTLMS = oc.FastForwardTTLMS
	o.ForwardedHeaders = oc.ForwardedHeaders
	o.HealthCheckUpstreamPath = oc.HealthCheckUpstreamPath
	o.HealthCheckVerb = oc.HealthCheckVerb
	o.HealthCheckQuery = oc.HealthCheckQuery
	o.Host = oc.Host
	o.Name = oc.Name
	o.IsDefault = oc.IsDefault
	o.KeepAliveTimeoutMS = oc.KeepAliveTimeoutMS
	o.MaxIdleConns = oc.MaxIdleConns
	o.MaxTTLMS = oc.MaxTTLMS
	o.MaxTTL = oc.MaxTTL
	o.MaxObjectSizeBytes = oc.MaxObjectSizeBytes
	o.MultipartRangesDisabled = oc.MultipartRangesDisabled
	o.Provider = oc.Provider
	o.OriginURL = oc.OriginURL
	o.PathPrefix = oc.PathPrefix
	o.ReqRewriterName = oc.ReqRewriterName
	o.RevalidationFactor = oc.RevalidationFactor
	o.RuleName = oc.RuleName
	o.Scheme = oc.Scheme
	o.Timeout = oc.Timeout
	o.TimeoutMS = oc.TimeoutMS
	o.TimeseriesRetention = oc.TimeseriesRetention
	o.TimeseriesRetentionFactor = oc.TimeseriesRetentionFactor
	o.TimeseriesEvictionMethodName = oc.TimeseriesEvictionMethodName
	o.TimeseriesEvictionMethod = oc.TimeseriesEvictionMethod
	o.TimeseriesTTL = oc.TimeseriesTTL
	o.TimeseriesTTLMS = oc.TimeseriesTTLMS
	o.ValueRetention = oc.ValueRetention

	o.TracingConfigName = oc.TracingConfigName

	if oc.Hosts != nil {
		o.Hosts = make([]string, len(oc.Hosts))
		copy(o.Hosts, oc.Hosts)
	}

	if oc.Hosts != nil {
		o.Hosts = make([]string, len(oc.Hosts))
		copy(o.Hosts, oc.Hosts)
	}

	if oc.CompressableTypeList != nil {
		o.CompressableTypeList = make([]string, len(oc.CompressableTypeList))
		copy(o.CompressableTypeList, oc.CompressableTypeList)
	}

	if oc.CompressableTypes != nil {
		o.CompressableTypes = make(map[string]bool)
		for k := range oc.CompressableTypes {
			o.CompressableTypes[k] = true
		}
	}

	if oc.HealthCheckHeaders != nil {
		o.HealthCheckHeaders = headers.Lookup(oc.HealthCheckHeaders).Clone()
	}

	o.Paths = make(map[string]*po.Options)
	for l, p := range oc.Paths {
		o.Paths[l] = p.Clone()
	}

	o.NegativeCacheName = oc.NegativeCacheName
	if oc.NegativeCache != nil {
		m := make(map[int]time.Duration)
		for c, t := range oc.NegativeCache {
			m[c] = t
		}
		o.NegativeCache = m
	}

	if oc.TLS != nil {
		o.TLS = oc.TLS.Clone()
	}
	o.RequireTLS = oc.RequireTLS

	if oc.FastForwardPath != nil {
		o.FastForwardPath = oc.FastForwardPath.Clone()
	}

	if oc.RuleOptions != nil {
		o.RuleOptions = oc.RuleOptions.Clone()
	}

	return o
}

// Validate validates the Lookup collection of Backend Options
func (l Lookup) Validate(ncl negative.Lookups) error {
	for k, o := range l {
		if o.Provider == "" {
			return fmt.Errorf(`missing provider for backend "%s"`, k)
		}
		if (o.Provider != "rule" && o.Provider != "alb") && o.OriginURL == "" {
			return fmt.Errorf(`missing origin-url for backend "%s"`, k)
		}
		url, err := url.Parse(o.OriginURL)
		if err != nil {
			return err
		}
		if strings.HasSuffix(url.Path, "/") {
			url.Path = url.Path[0 : len(url.Path)-1]
		}
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
		if o.CompressableTypeList != nil {
			o.CompressableTypes = make(map[string]bool)
			for _, v := range o.CompressableTypeList {
				o.CompressableTypes[v] = true
			}
		}
		if o.CacheKeyPrefix == "" {
			o.CacheKeyPrefix = o.Host
		}

		nc := ncl.Get(o.NegativeCacheName)
		if nc == nil {
			return fmt.Errorf(`invalid negative cache name: %s`, o.NegativeCacheName)
		}
		o.NegativeCache = nc

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
		return errors.New("invalid backend name:" + name)
	}
	return nil
}

// ValidateConfigMappings ensures that named config mappings from within origin configs
// (e.g., backends.cache_name) are valid
func (l Lookup) ValidateConfigMappings(rules ro.Lookup, caches co.Lookup) error {
	for k, oc := range l {
		if err := ValidateBackendName(k); err != nil {
			return err
		}
		switch oc.Provider {
		case "rule":
			// Rule Type Validations
			r, ok := rules[oc.RuleName]
			if !ok {
				return fmt.Errorf("invalid rule name [%s] provided in backend config [%s]", oc.RuleName, k)
			}
			r.Name = oc.RuleName
			oc.RuleOptions = r
		case "alb":
		default:
			if _, ok := caches[oc.CacheName]; !ok {
				return fmt.Errorf("invalid cache name [%s] provided in backend config [%s]", oc.CacheName, k)
			}
		}
	}
	return nil
}

// ValidateTLSConfigs iterates the map and validates any Options that use TLS
func (l Lookup) ValidateTLSConfigs() (bool, error) {
	var serveTLS bool
	for _, oc := range l {
		if oc.TLS != nil {
			b, err := oc.TLS.Validate()
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

// ProcessTOML iterates a TOML Config
func ProcessTOML(
	name string,
	options *Options,
	metadata *toml.MetaData,
	crw map[string]rewriter.RewriteInstructions,
	backends Lookup,
	activeCaches map[string]bool,
) (*Options, error) {

	if metadata == nil {
		return nil, errors.New("invalid config metadata")
	}

	oc := New()
	oc.Name = name

	if metadata.IsDefined("backends", name, "req_rewriter_name") && options.ReqRewriterName != "" {
		oc.ReqRewriterName = options.ReqRewriterName
		ri, ok := crw[oc.ReqRewriterName]
		if !ok {
			return nil, fmt.Errorf("invalid rewriter name %s in backend options %s",
				oc.ReqRewriterName, name)
		}
		oc.ReqRewriter = ri
	}

	if metadata.IsDefined("backends", name, "origin_type") {
		oc.Provider = options.Provider
	}

	if metadata.IsDefined("backends", name, "rule_name") {
		oc.RuleName = options.RuleName
	}

	if metadata.IsDefined("backends", name, "path_routing_disabled") {
		oc.PathRoutingDisabled = options.PathRoutingDisabled
	}

	if metadata.IsDefined("backends", name, "hosts") && options != nil {
		oc.Hosts = make([]string, len(options.Hosts))
		copy(oc.Hosts, options.Hosts)
	}

	if metadata.IsDefined("backends", name, "is_default") {
		oc.IsDefault = options.IsDefault
	}
	// If there is only one backend and is_default is not explicitly false, make it true
	if len(backends) == 1 && (!metadata.IsDefined("backends", name, "is_default")) {
		oc.IsDefault = true
	}

	if metadata.IsDefined("backends", name, "forwarded_headers") {
		oc.ForwardedHeaders = options.ForwardedHeaders
	}

	if metadata.IsDefined("backends", name, "require_tls") {
		oc.RequireTLS = options.RequireTLS
	}

	if metadata.IsDefined("backends", name, "cache_name") {
		oc.CacheName = options.CacheName
	}
	activeCaches[oc.CacheName] = true

	if metadata.IsDefined("backends", name, "cache_key_prefix") {
		oc.CacheKeyPrefix = options.CacheKeyPrefix
	}

	if metadata.IsDefined("backends", name, "origin_url") {
		oc.OriginURL = options.OriginURL
	}

	if metadata.IsDefined("backends", name, "compressable_types") {
		oc.CompressableTypeList = options.CompressableTypeList
	}

	if metadata.IsDefined("backends", name, "timeout_ms") {
		oc.TimeoutMS = options.TimeoutMS
	}

	if metadata.IsDefined("backends", name, "max_idle_conns") {
		oc.MaxIdleConns = options.MaxIdleConns
	}

	if metadata.IsDefined("backends", name, "keep_alive_timeout_ms") {
		oc.KeepAliveTimeoutMS = options.KeepAliveTimeoutMS
	}

	if metadata.IsDefined("backends", name, "timeseries_retention_factor") {
		oc.TimeseriesRetentionFactor = options.TimeseriesRetentionFactor
	}

	if metadata.IsDefined("backends", name, "timeseries_eviction_method") {
		oc.TimeseriesEvictionMethodName = strings.ToLower(options.TimeseriesEvictionMethodName)
		if p, ok := evictionmethods.Names[oc.TimeseriesEvictionMethodName]; ok {
			oc.TimeseriesEvictionMethod = p
		}
	}

	if metadata.IsDefined("backends", name, "timeseries_ttl_ms") {
		oc.TimeseriesTTLMS = options.TimeseriesTTLMS
	}

	if metadata.IsDefined("backends", name, "max_ttl_ms") {
		oc.MaxTTLMS = options.MaxTTLMS
	}

	if metadata.IsDefined("backends", name, "fastforward_ttl_ms") {
		oc.FastForwardTTLMS = options.FastForwardTTLMS
	}

	if metadata.IsDefined("backends", name, "fast_forward_disable") {
		oc.FastForwardDisable = options.FastForwardDisable
	}

	if metadata.IsDefined("backends", name, "backfill_tolerance_ms") {
		oc.BackfillToleranceMS = options.BackfillToleranceMS
	}

	if metadata.IsDefined("backends", name, "paths") {
		err := po.ProcessTOML(name, metadata, options.Paths, crw)
		if err != nil {
			return nil, err
		}
	}

	if metadata.IsDefined("backends", name, "negative_cache_name") {
		oc.NegativeCacheName = options.NegativeCacheName
	}

	if metadata.IsDefined("backends", name, "tracing_name") {
		oc.TracingConfigName = options.TracingConfigName
	}

	if metadata.IsDefined("backends", name, "health_check_upstream_path") {
		oc.HealthCheckUpstreamPath = options.HealthCheckUpstreamPath
	}

	if metadata.IsDefined("backends", name, "health_check_verb") {
		oc.HealthCheckVerb = options.HealthCheckVerb
	}

	if metadata.IsDefined("backends", name, "health_check_query") {
		oc.HealthCheckQuery = options.HealthCheckQuery
	}

	if metadata.IsDefined("backends", name, "health_check_headers") {
		oc.HealthCheckHeaders = options.HealthCheckHeaders
	}

	if metadata.IsDefined("backends", name, "max_object_size_bytes") {
		oc.MaxObjectSizeBytes = options.MaxObjectSizeBytes
	}

	if metadata.IsDefined("backends", name, "revalidation_factor") {
		oc.RevalidationFactor = options.RevalidationFactor
	}

	if metadata.IsDefined("backends", name, "multipart_ranges_disabled") {
		oc.MultipartRangesDisabled = options.MultipartRangesDisabled
	}

	if metadata.IsDefined("backends", name, "dearticulate_upstream_ranges") {
		oc.DearticulateUpstreamRanges = options.DearticulateUpstreamRanges
	}

	if metadata.IsDefined("backends", name, "tls") {
		oc.TLS = &to.Options{
			InsecureSkipVerify:        options.TLS.InsecureSkipVerify,
			CertificateAuthorityPaths: options.TLS.CertificateAuthorityPaths,
			PrivateKeyPath:            options.TLS.PrivateKeyPath,
			FullChainCertPath:         options.TLS.FullChainCertPath,
			ClientCertPath:            options.TLS.ClientCertPath,
			ClientKeyPath:             options.TLS.ClientKeyPath,
		}
	}

	return oc, nil
}
