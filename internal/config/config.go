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

// Package config provides Trickster configuration abilities, including
// parsing and printing configuration files, command line parameters, and
// environment variables, as well as default values and state.
package config

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/cache/evictionmethods"
	cache "github.com/Comcast/trickster/internal/cache/options"
	"github.com/Comcast/trickster/internal/cache/types"
	d "github.com/Comcast/trickster/internal/config/defaults"
	"github.com/Comcast/trickster/internal/proxy/headers"
	origins "github.com/Comcast/trickster/internal/proxy/origins/options"
	"github.com/Comcast/trickster/internal/proxy/paths/matching"
	to "github.com/Comcast/trickster/internal/proxy/tls/options"
	tracing "github.com/Comcast/trickster/internal/util/tracing/options"

	"github.com/BurntSushi/toml"
)

var providedOriginURL string
var providedOriginType string

// LoaderWarnings holds warnings generated during config load (before the logger is initialized),
// so they can be logged at the end of the loading process
var LoaderWarnings = make([]string, 0)

// TricksterConfig is the main configuration object
type TricksterConfig struct {
	// Main is the primary MainConfig section
	Main *MainConfig `toml:"main"`
	// Origins is a map of OriginConfigs
	Origins map[string]*origins.Options `toml:"origins"`
	// Caches is a map of CacheConfigs
	Caches map[string]*cache.Options `toml:"caches"`
	// ProxyServer is provides configurations about the Proxy Front End
	Frontend *FrontendConfig `toml:"frontend"`
	// Logging provides configurations that affect logging behavior
	Logging *LoggingConfig `toml:"logging"`
	// Metrics provides configurations for collecting Metrics about the application
	Metrics *MetricsConfig `toml:"metrics"`
	// TracingConfigs provides the distributed tracing configuration
	TracingConfigs map[string]*tracing.Options `toml:"tracing"`
	// NegativeCacheConfigs is a map of NegativeCacheConfigs
	NegativeCacheConfigs map[string]NegativeCacheConfig `toml:"negative_caches"`

	activeCaches map[string]bool
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `toml:"instance_id"`
	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `toml:"config_handler_path"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `toml:"ping_handler_path"`
	// ReloadConfig provides the details necessary to enable the config reloading feature of Trickster
	Reload *ReloadConfig `toml:"reload"`
}

// FrontendConfig is a collection of configurations for the main http frontend for the application
type FrontendConfig struct {
	// ListenAddress is IP address for the main http listener for the application
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port for the main http listener for the application
	ListenPort int `toml:"listen_port"`
	// TLSListenAddress is IP address for the tls  http listener for the application
	TLSListenAddress string `toml:"tls_listen_address"`
	// TLSListenPort is the TCP Port for the tls http listener for the application
	TLSListenPort int `toml:"tls_listen_port"`
	// ConnectionsLimit indicates how many concurrent front end connections trickster will handle at any time
	ConnectionsLimit int `toml:"connections_limit"`

	// ServeTLS indicates whether to listen and serve on the TLS port, meaning
	// at least one origin configuration has a valid certificate and key file configured.
	ServeTLS bool `toml:"-"`
}

// LoggingConfig is a collection of Logging configurations
type LoggingConfig struct {
	// LogFile provides the filepath to the instances's logfile. Set as empty string to Log to Console
	LogFile string `toml:"log_file"`
	// LogLevel provides the most granular level (e.g., DEBUG, INFO, ERROR) to log
	LogLevel string `toml:"log_level"`
}

// ReloadConfig is a collection of Metrics Collection configurations
type ReloadConfig struct {
	// ListenAddress is IP address from which the Reload API is available for triggering at /-/reload
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port from which the Reload API is available for triggering at /-/reload
	ListenPort int `toml:"listen_port"`
}

// MetricsConfig is a collection of Metrics Collection configurations
type MetricsConfig struct {
	// ListenAddress is IP address from which the Application Metrics are available for pulling at /metrics
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port from which the Application Metrics are available for pulling at /metrics
	ListenPort int `toml:"listen_port"`
}

// NegativeCacheConfig is a collection of response codes and their TTLs
type NegativeCacheConfig map[string]int

// Clone returns an exact copy of a NegativeCacheConfig
func (nc NegativeCacheConfig) Clone() NegativeCacheConfig {
	nc2 := make(NegativeCacheConfig)
	for k, v := range nc {
		nc2[k] = v
	}
	return nc2
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *TricksterConfig {
	return &TricksterConfig{
		Caches: map[string]*cache.Options{
			"default": cache.NewOptions(),
		},
		Logging: &LoggingConfig{
			LogFile:  d.DefaultLogFile,
			LogLevel: d.DefaultLogLevel,
		},
		Main: &MainConfig{
			ConfigHandlerPath: d.DefaultConfigHandlerPath,
			PingHandlerPath:   d.DefaultPingHandlerPath,
		},
		Metrics: &MetricsConfig{
			ListenPort: d.DefaultMetricsListenPort,
		},
		Origins: map[string]*origins.Options{
			"default": origins.NewOptions(),
		},
		Frontend: &FrontendConfig{
			ListenPort:       d.DefaultProxyListenPort,
			ListenAddress:    d.DefaultProxyListenAddress,
			TLSListenPort:    d.DefaultTLSProxyListenPort,
			TLSListenAddress: d.DefaultTLSProxyListenAddress,
		},
		NegativeCacheConfigs: map[string]NegativeCacheConfig{
			"default": NewNegativeCacheConfig(),
		},
		TracingConfigs: map[string]*tracing.Options{
			"default": tracing.NewOptions(),
		},
	}
}

// NewNegativeCacheConfig returns an empty NegativeCacheConfig
func NewNegativeCacheConfig() NegativeCacheConfig {
	return NegativeCacheConfig{}
}

// loadFile loads application configuration from a TOML-formatted file.
func (c *TricksterConfig) loadFile(flags *TricksterFlags) error {
	md, err := toml.DecodeFile(flags.ConfigPath, c)
	if err != nil {
		c.setDefaults(&toml.MetaData{})
		return err
	}
	err = c.setDefaults(&md)
	return err
}

func (c *TricksterConfig) setDefaults(metadata *toml.MetaData) error {

	var err error

	tracing.ProcessTracingConfigs(c.TracingConfigs, metadata)
	c.processOriginConfigs(metadata)
	c.processCachingConfigs(metadata)

	if err = c.validateConfigMappings(); err != nil {
		return err
	}

	if err = c.validateTLSConfigs(); err != nil {
		return err
	}

	return nil
}

func (c *TricksterConfig) validateTLSConfigs() error {
	for _, oc := range c.Origins {
		if oc.TLS != nil {
			b, err := oc.TLS.Validate()
			if err != nil {
				return err
			}
			if b {
				c.Frontend.ServeTLS = true
			}
		}
	}
	return nil
}

var pathMembers = []string{"path", "match_type", "handler", "methods", "cache_key_params", "cache_key_headers", "default_ttl_secs",
	"request_headers", "response_headers", "response_headers", "response_code", "response_body", "no_metrics", "progressive_collapsed_forwarding"}

func (c *TricksterConfig) validateConfigMappings() error {
	for k, oc := range c.Origins {
		// placeholder for feature being worked on different branch
		// if oc.OriginType == "rule" {
		// 	// Rule Type Validations
		// 	r, ok := c.Rules[oc.RuleName]
		// 	if !ok {
		// 		return fmt.Errorf("invalid rule name [%s] provided in origin config [%s]", oc.RuleName, k)
		// 	}
		// 	oc.RuleOptions = r
		// } else {
		// 	// non-Rule Type Validations
		if _, ok := c.Caches[oc.CacheName]; !ok {
			return fmt.Errorf("invalid cache name [%s] provided in origin config [%s]", oc.CacheName, k)
		}
		// }
	}
	return nil
}

func (c *TricksterConfig) processOriginConfigs(metadata *toml.MetaData) {

	c.activeCaches = make(map[string]bool)

	for k, v := range c.Origins {

		oc := origins.NewOptions()
		oc.Name = k

		if metadata.IsDefined("origins", k, "origin_type") {
			oc.OriginType = v.OriginType
		}

		if metadata.IsDefined("origins", k, "rule_name") {
			oc.RuleName = v.RuleName
		}

		if metadata.IsDefined("origins", k, "hosts") && v != nil {
			oc.Hosts = make([]string, len(v.Hosts))
			copy(oc.Hosts, v.Hosts)
		}

		if metadata.IsDefined("origins", k, "is_default") {
			oc.IsDefault = v.IsDefault
		}
		// If there is only one origin and is_default is not explicitly false, make it true
		if len(c.Origins) == 1 && (!metadata.IsDefined("origins", k, "is_default")) {
			oc.IsDefault = true
		}

		if metadata.IsDefined("origins", k, "require_tls") {
			oc.RequireTLS = v.RequireTLS
		}

		if metadata.IsDefined("origins", k, "cache_name") {
			oc.CacheName = v.CacheName
		}
		c.activeCaches[oc.CacheName] = true

		if metadata.IsDefined("origins", k, "cache_key_prefix") {
			oc.CacheKeyPrefix = v.CacheKeyPrefix
		}

		if metadata.IsDefined("origins", k, "origin_url") {
			oc.OriginURL = v.OriginURL
		}

		if metadata.IsDefined("origins", k, "compressable_types") {
			oc.CompressableTypeList = v.CompressableTypeList
		}

		if metadata.IsDefined("origins", k, "timeout_secs") {
			oc.TimeoutSecs = v.TimeoutSecs
		}

		if metadata.IsDefined("origins", k, "max_idle_conns") {
			oc.MaxIdleConns = v.MaxIdleConns
		}

		if metadata.IsDefined("origins", k, "keep_alive_timeout_secs") {
			oc.KeepAliveTimeoutSecs = v.KeepAliveTimeoutSecs
		}

		if metadata.IsDefined("origins", k, "timeseries_retention_factor") {
			oc.TimeseriesRetentionFactor = v.TimeseriesRetentionFactor
		}

		if metadata.IsDefined("origins", k, "timeseries_eviction_method") {
			oc.TimeseriesEvictionMethodName = strings.ToLower(v.TimeseriesEvictionMethodName)
			if p, ok := evictionmethods.Names[oc.TimeseriesEvictionMethodName]; ok {
				oc.TimeseriesEvictionMethod = p
			}
		}

		if metadata.IsDefined("origins", k, "timeseries_ttl_secs") {
			oc.TimeseriesTTLSecs = v.TimeseriesTTLSecs
		}

		if metadata.IsDefined("origins", k, "max_ttl_secs") {
			oc.MaxTTLSecs = v.MaxTTLSecs
		}

		if metadata.IsDefined("origins", k, "fastforward_ttl_secs") {
			oc.FastForwardTTLSecs = v.FastForwardTTLSecs
		}

		if metadata.IsDefined("origins", k, "fast_forward_disable") {
			oc.FastForwardDisable = v.FastForwardDisable
		}

		if metadata.IsDefined("origins", k, "backfill_tolerance_secs") {
			oc.BackfillToleranceSecs = v.BackfillToleranceSecs
		}

		if metadata.IsDefined("origins", k, "paths") {
			var j = 0
			for l, p := range v.Paths {
				if len(p.Methods) == 0 {
					p.Methods = []string{http.MethodGet, http.MethodHead}
				}
				p.Custom = make([]string, 0)
				for _, pm := range pathMembers {
					if metadata.IsDefined("origins", k, "paths", l, pm) {
						p.Custom = append(p.Custom, pm)
					}
				}
				if metadata.IsDefined("origins", k, "paths", l, "response_body") {
					p.ResponseBodyBytes = []byte(p.ResponseBody)
					p.HasCustomResponseBody = true
				}

				if mt, ok := matching.Names[strings.ToLower(p.MatchTypeName)]; ok {
					p.MatchType = mt
					p.MatchTypeName = p.MatchType.String()
				} else {
					p.MatchType = matching.PathMatchTypeExact
					p.MatchTypeName = p.MatchType.String()
				}
				oc.Paths[p.Path+"-"+strings.Join(p.Methods, "-")] = p
				j++
			}
		}

		if metadata.IsDefined("origins", k, "negative_cache_name") {
			oc.NegativeCacheName = v.NegativeCacheName
		}

		if metadata.IsDefined("origins", k, "tracing_name") {
			oc.TracingConfigName = v.TracingConfigName
		}

		if metadata.IsDefined("origins", k, "health_check_upstream_path") {
			oc.HealthCheckUpstreamPath = v.HealthCheckUpstreamPath
		}

		if metadata.IsDefined("origins", k, "health_check_verb") {
			oc.HealthCheckVerb = v.HealthCheckVerb
		}

		if metadata.IsDefined("origins", k, "health_check_query") {
			oc.HealthCheckQuery = v.HealthCheckQuery
		}

		if metadata.IsDefined("origins", k, "health_check_headers") {
			oc.HealthCheckHeaders = v.HealthCheckHeaders
		}

		if metadata.IsDefined("origins", k, "max_object_size_bytes") {
			oc.MaxObjectSizeBytes = v.MaxObjectSizeBytes
		}

		if metadata.IsDefined("origins", k, "revalidation_factor") {
			oc.RevalidationFactor = v.RevalidationFactor
		}

		if metadata.IsDefined("origins", k, "multipart_ranges_disabled") {
			oc.MultipartRangesDisabled = v.MultipartRangesDisabled
		}

		if metadata.IsDefined("origins", k, "dearticulate_upstream_ranges") {
			oc.DearticulateUpstreamRanges = v.DearticulateUpstreamRanges
		}

		if metadata.IsDefined("origins", k, "tls") {
			oc.TLS = &to.Options{
				InsecureSkipVerify:        v.TLS.InsecureSkipVerify,
				CertificateAuthorityPaths: v.TLS.CertificateAuthorityPaths,
				PrivateKeyPath:            v.TLS.PrivateKeyPath,
				FullChainCertPath:         v.TLS.FullChainCertPath,
				ClientCertPath:            v.TLS.ClientCertPath,
				ClientKeyPath:             v.TLS.ClientKeyPath,
			}
		}

		c.Origins[k] = oc
	}
}

func (c *TricksterConfig) processCachingConfigs(metadata *toml.MetaData) {

	// setCachingDefaults assumes that processOriginConfigs was just ran

	for k, v := range c.Caches {

		if _, ok := c.activeCaches[k]; !ok {
			// a configured cache was not used by any origin. don't even instantiate it
			delete(c.Caches, k)
			continue
		}

		cc := cache.NewOptions()
		cc.Name = k

		if metadata.IsDefined("caches", k, "cache_type") {
			cc.CacheType = strings.ToLower(v.CacheType)
			if n, ok := types.Names[cc.CacheType]; ok {
				cc.CacheTypeID = n
			}
		}

		if metadata.IsDefined("caches", k, "index", "reap_interval_secs") {
			cc.Index.ReapIntervalSecs = v.Index.ReapIntervalSecs
		}

		if metadata.IsDefined("caches", k, "index", "flush_interval_secs") {
			cc.Index.FlushIntervalSecs = v.Index.FlushIntervalSecs
		}

		if metadata.IsDefined("caches", k, "index", "max_size_bytes") {
			cc.Index.MaxSizeBytes = v.Index.MaxSizeBytes
		}

		if metadata.IsDefined("caches", k, "index", "max_size_backoff_bytes") {
			cc.Index.MaxSizeBackoffBytes = v.Index.MaxSizeBackoffBytes
		}

		if metadata.IsDefined("caches", k, "index", "max_size_objects") {
			cc.Index.MaxSizeObjects = v.Index.MaxSizeObjects
		}

		if metadata.IsDefined("caches", k, "index", "max_size_backoff_objects") {
			cc.Index.MaxSizeBackoffObjects = v.Index.MaxSizeBackoffObjects
		}

		if cc.CacheTypeID == types.CacheTypeRedis {

			var hasEndpoint, hasEndpoints bool

			ct := strings.ToLower(v.Redis.ClientType)
			if metadata.IsDefined("caches", k, "redis", "client_type") {
				cc.Redis.ClientType = ct
			}

			if metadata.IsDefined("caches", k, "redis", "protocol") {
				cc.Redis.Protocol = v.Redis.Protocol
			}

			if metadata.IsDefined("caches", k, "redis", "endpoint") {
				cc.Redis.Endpoint = v.Redis.Endpoint
				hasEndpoint = true
			}

			if metadata.IsDefined("caches", k, "redis", "endpoints") {
				cc.Redis.Endpoints = v.Redis.Endpoints
				hasEndpoints = true
			}

			if cc.Redis.ClientType == "standard" {
				if hasEndpoints && !hasEndpoint {
					LoaderWarnings = append(LoaderWarnings, "'standard' redis type configured, but 'endpoints' value is provided instead of 'endpoint'")
				}
			} else {
				if hasEndpoint && !hasEndpoints {
					LoaderWarnings = append(LoaderWarnings, fmt.Sprintf("'%s' redis type configured, but 'endpoint' value is provided instead of 'endpoints'", cc.Redis.ClientType))
				}
			}

			if metadata.IsDefined("caches", k, "redis", "sentinel_master") {
				cc.Redis.SentinelMaster = v.Redis.SentinelMaster
			}

			if metadata.IsDefined("caches", k, "redis", "password") {
				cc.Redis.Password = v.Redis.Password
			}

			if metadata.IsDefined("caches", k, "redis", "db") {
				cc.Redis.DB = v.Redis.DB
			}

			if metadata.IsDefined("caches", k, "redis", "max_retries") {
				cc.Redis.MaxRetries = v.Redis.MaxRetries
			}

			if metadata.IsDefined("caches", k, "redis", "min_retry_backoff_ms") {
				cc.Redis.MinRetryBackoffMS = v.Redis.MinRetryBackoffMS
			}

			if metadata.IsDefined("caches", k, "redis", "max_retry_backoff_ms") {
				cc.Redis.MaxRetryBackoffMS = v.Redis.MaxRetryBackoffMS
			}

			if metadata.IsDefined("caches", k, "redis", "dial_timeout_ms") {
				cc.Redis.DialTimeoutMS = v.Redis.DialTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "read_timeout_ms") {
				cc.Redis.ReadTimeoutMS = v.Redis.ReadTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "write_timeout_ms") {
				cc.Redis.WriteTimeoutMS = v.Redis.WriteTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "pool_size") {
				cc.Redis.PoolSize = v.Redis.PoolSize
			}

			if metadata.IsDefined("caches", k, "redis", "min_idle_conns") {
				cc.Redis.MinIdleConns = v.Redis.MinIdleConns
			}

			if metadata.IsDefined("caches", k, "redis", "max_conn_age_ms") {
				cc.Redis.MaxConnAgeMS = v.Redis.MaxConnAgeMS
			}

			if metadata.IsDefined("caches", k, "redis", "pool_timeout_ms") {
				cc.Redis.PoolTimeoutMS = v.Redis.PoolTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "idle_timeout_ms") {
				cc.Redis.IdleTimeoutMS = v.Redis.IdleTimeoutMS
			}

			if metadata.IsDefined("caches", k, "redis", "idle_check_frequency_ms") {
				cc.Redis.IdleCheckFrequencyMS = v.Redis.IdleCheckFrequencyMS
			}
		}

		if metadata.IsDefined("caches", k, "filesystem", "cache_path") {
			cc.Filesystem.CachePath = v.Filesystem.CachePath
		}

		if metadata.IsDefined("caches", k, "bbolt", "filename") {
			cc.BBolt.Filename = v.BBolt.Filename
		}

		if metadata.IsDefined("caches", k, "bbolt", "bucket") {
			cc.BBolt.Bucket = v.BBolt.Bucket
		}

		if metadata.IsDefined("caches", k, "badger", "directory") {
			cc.Badger.Directory = v.Badger.Directory
		}

		if metadata.IsDefined("caches", k, "badger", "value_directory") {
			cc.Badger.ValueDirectory = v.Badger.ValueDirectory
		}

		c.Caches[k] = cc
	}
}

func (c *TricksterConfig) copy() *TricksterConfig {

	nc := NewConfig()
	delete(nc.Caches, "default")
	delete(nc.Origins, "default")

	nc.Main.ConfigHandlerPath = c.Main.ConfigHandlerPath
	nc.Main.InstanceID = c.Main.InstanceID
	nc.Main.PingHandlerPath = c.Main.PingHandlerPath

	nc.Logging.LogFile = c.Logging.LogFile
	nc.Logging.LogLevel = c.Logging.LogLevel

	nc.Metrics.ListenAddress = c.Metrics.ListenAddress
	nc.Metrics.ListenPort = c.Metrics.ListenPort

	nc.Frontend.ListenAddress = c.Frontend.ListenAddress
	nc.Frontend.ListenPort = c.Frontend.ListenPort
	nc.Frontend.TLSListenAddress = c.Frontend.TLSListenAddress
	nc.Frontend.TLSListenPort = c.Frontend.TLSListenPort
	nc.Frontend.ConnectionsLimit = c.Frontend.ConnectionsLimit
	nc.Frontend.ServeTLS = c.Frontend.ServeTLS

	for k, v := range c.Origins {
		nc.Origins[k] = v.Clone()
	}

	for k, v := range c.Caches {
		nc.Caches[k] = v.Clone()
	}

	for k, v := range c.NegativeCacheConfigs {
		nc.NegativeCacheConfigs[k] = v.Clone()
	}

	for k, v := range c.TracingConfigs {
		nc.TracingConfigs[k] = v.Clone()
	}

	return nc
}

func (c *TricksterConfig) String() string {
	cp := c.copy()

	// the toml library will panic if the Handler is assigned,
	// even though this field is annotated as skip ("-") in the prototype
	// so we'll iterate the paths and set to nil the Handler (in our local copy only)
	if cp.Origins != nil {
		for _, v := range cp.Origins {
			if v != nil {
				for _, w := range v.Paths {
					w.Handler = nil
					w.KeyHasher = nil
				}
			}
			// also strip out potentially sensitive headers
			hideAuthorizationCredentials(v.HealthCheckHeaders)

			if v.Paths != nil {
				for _, p := range v.Paths {
					hideAuthorizationCredentials(p.RequestHeaders)
					hideAuthorizationCredentials(p.ResponseHeaders)
				}
			}
		}
	}

	// strip Redis password
	for k, v := range cp.Caches {
		if v != nil && cp.Caches[k].Redis.Password != "" {
			cp.Caches[k].Redis.Password = "*****"
		}
	}

	var buf bytes.Buffer
	e := toml.NewEncoder(&buf)
	e.Encode(cp)
	return buf.String()
}

var sensitiveCredentials = map[string]bool{headers.NameAuthorization: true}

func hideAuthorizationCredentials(headers map[string]string) {
	// strip Authorization Headers
	for k := range headers {
		if _, ok := sensitiveCredentials[k]; ok {
			headers[k] = "*****"
		}
	}
}
