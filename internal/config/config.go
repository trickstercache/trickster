/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the Running Configuration for Trickster
var Config *TricksterConfig

// Main is the Main subsection of the Running Configuration
var Main *MainConfig

// Origins is the Origin Map subsection of the Running Configuration
var Origins map[string]*OriginConfig

// Caches is the Cache Map subsection of the Running Configuration
var Caches map[string]*CachingConfig

// ProxyServer is the Proxy Server subsection of the Running Configuration
var ProxyServer *ProxyServerConfig

// Logging is the Logging subsection of the Running Configuration
var Logging *LoggingConfig

// Metrics is the Metrics subsection of the Running Configuration
var Metrics *MetricsConfig

// Flags is a collection of command line flags that Trickster loads.
var Flags = TricksterFlags{}
var providedOriginURL string
var providedOriginType string

// ApplicationName is the name of the Application
var ApplicationName string

// ApplicationVersion holds the version of the Application
var ApplicationVersion string

// LoaderWarnings holds warnings generated during config load (before the logger is initialized),
// so they can be logged at the end of the loading process
var LoaderWarnings = make([]string, 0, 0)

// TricksterConfig is the main configuration object
type TricksterConfig struct {
	Main        *MainConfig               `toml:"main"`
	Origins     map[string]*OriginConfig  `toml:"origins"`
	Caches      map[string]*CachingConfig `toml:"caches"`
	ProxyServer *ProxyServerConfig        `toml:"proxy_server"`
	Logging     *LoggingConfig            `toml:"logging"`
	Metrics     *MetricsConfig            `toml:"metrics"`

	activeCaches map[string]bool
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `toml:"instance_id"`
	// Environment indicates the operating environment of the running instance (e.g., "dev", "stage", "prod")
	Environment string
	// Hostname is populated with the self-resolved Hostname where the instance is running
	Hostname string

	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `toml:"config_handler_path"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `toml:"ping_handler_path"`
}

// OriginConfig is a collection of configurations for prometheus origins proxied by Trickster
type OriginConfig struct {

	// HTTP and Proxy Configurations
	//
	// IsDefault indicates if this is the default origin for any request not matching a configured route
	IsDefault bool `toml:"is_default"`
	// OriginType describes the type of origin (e.g., 'prometheus')
	OriginType string `toml:"origin_type"`
	// OriginURL provides the base upstream URL for all proxied requests to this origin.
	// it can be as simple as http://example.com or as complex as https://example.com:8443/path/prefix
	OriginURL string `toml:"origin_url"`
	// TimeoutSecs defines how long the HTTP request will wait for a response before timing out
	TimeoutSecs int64 `toml:"timeout_secs"`
	// KeepAliveTimeoutSecs defines how long an open keep-alive HTTP connection remains idle before closing
	KeepAliveTimeoutSecs int64 `toml:"keep_alive_timeout_secs"`
	// MaxIdleConns defines maximum number of open keep-alive connections to maintain
	MaxIdleConns int `toml:"max_idle_conns"`
	// CacheName provides the name of the configured cache where the origin client will store it's cache data
	CacheName string `toml:"cache_name"`
	// IgnoreCachingHeaders will cause the orgin client to ignore any upstream or downstream HTTP caching headers
	IgnoreCachingHeaders bool `toml:"ignore_caching_headers"`
	// HealthCheckEndpoint provides the route path Trickster will register for mapping the Health Endpoint
	HealthCheckEndpoint string `toml:"health_check_endpoint"`
	// HealthCheckUpstreamPath provides the URL path for the upstream health check
	HealthCheckUpstreamPath string `toml:"health_check_upstream_path"`
	// HealthCheckVerb provides the HTTP verb to use when making an upstream health check
	HealthCheckVerb string `toml:"health_check_verb"`
	// HealthCheckQuery provides the HTTP query parameters to use when making an upstream health check
	HealthCheckQuery string `toml:"health_check_query"`
	// Object Proxy Cache and Delta Proxy Cache Configurations
	// TimeseriesRetentionFactor limits the maxiumum the number of chronological timestamps worth of data to store in cache for each query
	TimeseriesRetentionFactor int `toml:"timeseries_retention_factor"`
	// FastForwardDisable indicates whether the FastForward feature should be disabled for this origin
	FastForwardDisable bool `toml:"fast_forward_disable"`
	// BackfillToleranceSecs prevents values with timestamps newer than the provided number of seconds from being cached
	// this allows propagation of upstream backfill operations that modify recently-served data
	BackfillToleranceSecs int64 `toml:"backfill_tolerance_secs"`
	// PathList is a list of ProxyPathConfigs that control the behavior of the given paths when requested
	Paths map[string]*ProxyPathConfig `toml:"paths"`
	// NegativeCache is a map of HTTP Status Codes that are cached for the provided duration, usually used for failures (e.g., 404's for 10s)
	NegativeCacheSecs map[int]int `toml:"negative_cache"`
	// TimeseriesEvictionMethod

	// Synthesized Configurations
	// These configurations are parsed versions of those defined above, and are what Trickster uses internally
	//
	// Name is the Name of the origin, taken from the Key in the Origins map[string]*OriginConfig
	Name string `toml:"-"`
	// Timeout is the time.Duration representation of TimeoutSecs
	Timeout time.Duration `toml:"-"`
	// BackfillTolerance is the time.Duration representation of BackfillToleranceSecs
	BackfillTolerance time.Duration `toml:"-"`
	// ValueRetention is the time.Duration representation of ValueRetentionSecs
	ValueRetention time.Duration `toml:"-"`
	// Scheme is the layer 7 protocol indicator (e.g. 'http'), derived from OriginURL
	Scheme string `toml:"-"`
	// Host is the upstream hostname/IP[:port] the origin client will connect to when fetching uncached data, derived from OriginURL
	Host string `toml:"-"`
	// PathPrefix provides any prefix added to the front of the requested path when constructing the upstream request url, derived from OriginURL
	PathPrefix string `toml:"-"`
	// NegativeCache provides a map for the negative cache, with TTLs converted to time.Durations
	NegativeCache map[int]time.Duration `toml:"-"`

	TimeseriesEvictionMethodName string

	TimeseriesRetention      time.Duration            `toml:"-"`
	TimeseriesEvictionMethod TimeseriesEvictionMethod `toml:"-"`
}

// ProxyPathConfig ...
type ProxyPathConfig struct {
	// Path indicates the HTTP Request's URL PATH to which this configuration applies
	Path string `toml:"path"`
	// HandlerName provides the name of the HTTP handler to use
	HandlerName string `toml:"handler"`
	// Methods provides the list of permitted HTTP request methods for this Path
	Methods []string `toml:"methods"`
	// CacheKeyParams provides the list of http request query parameters to be included in the hash for each query's cache key
	CacheKeyParams []string `toml:"cache_key_params"`
	// CacheKeyHeaders provides the list of http request headers to be included in the hash for each query's cache key
	CacheKeyHeaders []string `toml:"cache_key_headers"`
	// DefaultTTLSecs indicates the TTL Cache for this path. If
	DefaultTTLSecs int `toml:"default_ttl_secs"`
	// RequestHeaders is a map of headers that will be added to requests to the upstream Origin for this path
	RequestHeaders map[string]string `toml:"request_headers"`
	// ResponseHeaders is a map of http headers that will be added to responses to the downstream client
	ResponseHeaders map[string]string `toml:"response_headers"`
	// ResponseCode sets a custom response code to be sent to downstream clients for this path.
	ResponseCode int `toml:"response_code"`
	// ResponseBody sets a custom response body to be sent to the donstream client for this path.
	ResponseBody string `toml:"response_body"`

	// Synthesized ProxyPathConfig Values
	//
	// DefaultTTL is the time.Duration representation of DefaultTTLSecs
	DefaultTTL time.Duration `toml:"-"`
	// Handler is a pointer to the HTTP Handler method represented by the HandlerName
	Handler func(w http.ResponseWriter, r *http.Request) `toml:"-"`
	// Order is this Path's order index in the list of configured Paths
	Order int `toml:"-"`
	// HasCustomResponseBody is a boolean indicating if the response body is custom
	// this flag allows an empty string response to be configured as a return value
	HasCustomResponseBody bool `toml:"-"`
	// ResponseBodyBytes provides a byte slice version of the ResponseBody value
	ResponseBodyBytes []byte `toml:"-"`
}

// CachingConfig is a collection of defining the Trickster Caching Behavior
type CachingConfig struct {
	// Type represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	CacheType          string                `toml:"cache_type"`
	Compression        bool                  `toml:"compression"`
	TimeseriesTTLSecs  int                   `toml:"timeseries_ttl_secs"`
	ObjectTTLSecs      int                   `toml:"object_ttl_secs"`
	FastForwardTTLSecs int                   `toml:"fastforward_ttl_secs"`
	MaxObjectSizeBytes int                   `toml:"max_object_size_bytes"`
	Index              CacheIndexConfig      `toml:"index"`
	Redis              RedisCacheConfig      `toml:"redis"`
	Filesystem         FilesystemCacheConfig `toml:"filesystem"`
	BBolt              BBoltCacheConfig      `toml:"bbolt"`
	Badger             BadgerCacheConfig     `toml:"badger"`

	TimeseriesTTL  time.Duration `toml:"-"`
	ObjectTTL      time.Duration `toml:"-"`
	FastForwardTTL time.Duration `toml:"-"`
}

// CacheIndexConfig defines the operation of the Cache Indexer
type CacheIndexConfig struct {
	ReapIntervalSecs      int   `toml:"reap_interval_secs"`
	FlushIntervalSecs     int   `toml:"flush_interval_secs"`
	MaxSizeBytes          int64 `toml:"max_size_bytes"`
	MaxSizeBackoffBytes   int64 `toml:"max_size_backoff_bytes"`
	MaxSizeObjects        int64 `toml:"max_size_objects"`
	MaxSizeBackoffObjects int64 `toml:"max_size_backoff_objects"`

	ReapInterval  time.Duration `toml:"-"`
	FlushInterval time.Duration `toml:"-"`
}

// RedisCacheConfig is a collection of Configurations for Connecting to Redis
type RedisCacheConfig struct {
	// ClientType defines the type of Redis Client ("standard", "cluster", "sentinel")
	ClientType string `toml:"client_type"`
	// Protocol represents the connection method (e.g., "tcp", "unix", etc.)
	Protocol string `toml:"protocol"`
	// Endpoint represents FQDN:port or IPAddress:Port of the Redis/Sentinel Endpoint
	Endpoint string `toml:"endpoint"`
	// Endpoints represents FQDN:port or IPAddress:Port collection of a Redis Cluster
	Endpoints []string `toml:"endpoints"`
	// Password can be set when using password protected redis instance.
	Password string `toml:"password"`
	// SentinelMaster should be set when using Redis Sentinel to indicate the Master Node
	SentinelMaster string `toml:"sentinel_master"`
	// DB is the Database to be selected after connecting to the server.
	DB int `toml:"db"`
	// Maximum number of retries before giving up on the command
	MaxRetries int `toml:"max_retries"`
	// Minimum backoff between each retry.
	MinRetryBackoffMS int `toml:"min_retry_backoff_ms"`
	// MaxRetryBackoffMS is the Maximum backoff between each retry.
	MaxRetryBackoffMS int `toml:"max_retry_backoff_ms"`
	// DialTimeoutMS is the timeout for establishing new connections.
	DialTimeoutMS int `toml:"dial_timeout_ms"`
	// ReadTimeoutMS is the timeout for socket reads. If reached, commands will fail with a timeout instead of blocking.
	ReadTimeoutMS int `toml:"read_timeout_ms"`
	// WriteTimeoutMS is the timeout for socket writes. If reached, commands will fail with a timeout instead of blocking.
	WriteTimeoutMS int `toml:"write_timeout_ms"`
	// PoolSize is the maximum number of socket connections.
	PoolSize int `toml:"pool_size"`
	// MinIdleConns is the minimum number of idle connections which is useful when establishing new connection is slow.
	MinIdleConns int `toml:"min_idle_conns"`
	// MaxConnAgeMS is the connection age at which client retires (closes) the connection.
	MaxConnAgeMS int `toml:"max_conn_age_ms"`
	// PoolTimeoutMS is the amount of time client waits for connection if all connections are busy before returning an error.
	PoolTimeoutMS int `toml:"pool_timeout_ms"`
	// IdleTimeoutMS is the amount of time after which client closes idle connections.
	IdleTimeoutMS int `toml:"idle_timeout_ms"`
	// IdleCheckFrequencyMS is the frequency of idle checks made by idle connections reaper.
	IdleCheckFrequencyMS int `toml:"idle_check_frequency_ms"`
}

// BadgerCacheConfig is a collection of Configurations for storing cached data on the Filesystem in a Badger key-value store
type BadgerCacheConfig struct {
	// Directory represents the path on disk where the Badger database should store data
	Directory string `toml:"directory"`
	// ValueDirectory represents the path on disk where the Badger database will store its value log.
	ValueDirectory string `toml:"value_directory"`
}

// BBoltCacheConfig is a collection of Configurations for storing cached data on the Filesystem
type BBoltCacheConfig struct {
	// Filename represents the filename (including path) of the BotlDB database
	Filename string `toml:"filename"`
	// Bucket represents the name of the bucket within BBolt under which Trickster's keys will be stored.
	Bucket string `toml:"bucket"`
}

// FilesystemCacheConfig is a collection of Configurations for storing cached data on the Filesystem
type FilesystemCacheConfig struct {
	// CachePath represents the path on disk where our cache will live
	CachePath string `toml:"cache_path"`
}

// ProxyServerConfig is a collection of configurations for the main http listener for the application
type ProxyServerConfig struct {
	// ListenAddress is IP address for the main http listener for the application
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port for the main http listener for the application
	ListenPort int `toml:"listen_port"`
	// ConnectionsLimit indicates how many concurrent front end connections trickster will handle at any time
	ConnectionsLimit int `toml:"connections_limit"`
}

// LoggingConfig is a collection of Logging configurations
type LoggingConfig struct {
	// LogFile provides the filepath to the instances's logfile. Set as empty string to Log to Console
	LogFile string `toml:"log_file"`
	// LogLevel provides the most granular level (e.g., DEBUG, INFO, ERROR) to log
	LogLevel string `toml:"log_level"`
}

// MetricsConfig is a collection of Metrics Collection configurations
type MetricsConfig struct {
	// ListenAddress is IP address from which the Application Metrics are available for pulling at /metrics
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port from which the Application Metrics are available for pulling at /metrics
	ListenPort int `toml:"listen_port"`
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *TricksterConfig {
	return &TricksterConfig{
		Caches: map[string]*CachingConfig{
			"default": NewCacheConfig(),
		},
		Logging: &LoggingConfig{
			LogFile:  defaultLogFile,
			LogLevel: defaultLogLevel,
		},
		Main: &MainConfig{
			Hostname:          defaultHostname,
			ConfigHandlerPath: defaultConfigHandlerPath,
			PingHandlerPath:   defaultPingHandlerPath,
		},
		Metrics: &MetricsConfig{
			ListenPort: defaultMetricsListenPort,
		},
		Origins: map[string]*OriginConfig{
			"default": NewOriginConfig(),
		},
		ProxyServer: &ProxyServerConfig{
			ListenPort: defaultProxyListenPort,
		},
	}
}

// NewCacheConfig will return a pointer to an OriginConfig with the default configuration settings
func NewCacheConfig() *CachingConfig {

	return &CachingConfig{
		CacheType:          defaultCacheType,
		Compression:        defaultCacheCompression,
		TimeseriesTTLSecs:  defaultTimeseriesTTLSecs,
		FastForwardTTLSecs: defaultFastForwardTTLSecs,
		MaxObjectSizeBytes: defaultMaxObjectSizeBytes,
		ObjectTTLSecs:      defaultObjectTTLSecs,
		Redis:              RedisCacheConfig{ClientType: defaultRedisClientType, Protocol: defaultRedisProtocol, Endpoint: defaultRedisEndpoint, Endpoints: []string{defaultRedisEndpoint}},
		Filesystem:         FilesystemCacheConfig{CachePath: defaultCachePath},
		BBolt:              BBoltCacheConfig{Filename: defaultBBoltFile, Bucket: defaultBBoltBucket},
		Badger:             BadgerCacheConfig{Directory: defaultCachePath, ValueDirectory: defaultCachePath},
		Index: CacheIndexConfig{
			ReapIntervalSecs:      defaultCacheIndexReap,
			FlushIntervalSecs:     defaultCacheIndexFlush,
			MaxSizeBytes:          defaultCacheMaxSizeBytes,
			MaxSizeBackoffBytes:   defaultMaxSizeBackoffBytes,
			MaxSizeObjects:        defaultMaxSizeObjects,
			MaxSizeBackoffObjects: defaultMaxSizeBackoffObjects,
		},
	}
}

// NewOriginConfig will return a pointer to an OriginConfig with the default configuration settings
func NewOriginConfig() *OriginConfig {
	return &OriginConfig{
		BackfillTolerance:            defaultBackfillToleranceSecs,
		BackfillToleranceSecs:        defaultBackfillToleranceSecs,
		CacheName:                    defaultOriginCacheName,
		HealthCheckEndpoint:          defaultHealthEndpoint,
		HealthCheckQuery:             defaultHealthCheckQuery,
		HealthCheckUpstreamPath:      defaultHealthCheckPath,
		HealthCheckVerb:              defaultHealthCheckVerb,
		IgnoreCachingHeaders:         defaultOriginINCH,
		KeepAliveTimeoutSecs:         defaultKeepAliveTimeoutSecs,
		MaxIdleConns:                 defaultMaxIdleConns,
		NegativeCache:                make(map[int]time.Duration),
		NegativeCacheSecs:            make(map[int]int),
		Paths:                        make(map[string]*ProxyPathConfig),
		Timeout:                      time.Second * defaultOriginTimeoutSecs,
		TimeoutSecs:                  defaultOriginTimeoutSecs,
		TimeseriesEvictionMethod:     defaultOriginTEM,
		TimeseriesEvictionMethodName: defaultOriginTEMName,
		TimeseriesRetention:          defaultOriginTRF,
		TimeseriesRetentionFactor:    defaultOriginTRF, // Cache a max of 1024 recent timestamps of data for each query
		//OriginType:                   defaultOriginServerType,
	}
}

// loadFile loads application configuration from a TOML-formatted file.
func (c *TricksterConfig) loadFile() error {
	md, err := toml.DecodeFile(Flags.ConfigPath, c)
	c.setDefaults(md)
	return err
}

func (c *TricksterConfig) setDefaults(metadata toml.MetaData) {
	c.setOriginDefaults(metadata)
	c.setCachingDefaults(metadata)
}

func (c *TricksterConfig) setOriginDefaults(metadata toml.MetaData) {

	c.activeCaches = make(map[string]bool)

	for k, v := range c.Origins {

		oc := NewOriginConfig()
		if metadata.IsDefined("origins", k, "origin_type") {
			oc.OriginType = v.OriginType
		}

		if metadata.IsDefined("origins", k, "is_default") {
			oc.IsDefault = v.IsDefault
		}
		// If there is only one origin and is_default is not explicitly false, make it true
		if len(c.Origins) == 1 && (!metadata.IsDefined("origins", k, "is_default")) {
			oc.IsDefault = true
		}

		if metadata.IsDefined("origins", k, "cache_name") {
			oc.CacheName = v.CacheName
		}

		c.activeCaches[oc.CacheName] = true

		if metadata.IsDefined("origins", k, "origin_url") {
			oc.OriginURL = v.OriginURL
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

		if metadata.IsDefined("origins", k, "ignore_caching_headers") {
			oc.IgnoreCachingHeaders = v.IgnoreCachingHeaders
		}

		if metadata.IsDefined("origins", k, "timeseries_retention_factor") {
			oc.TimeseriesRetentionFactor = v.TimeseriesRetentionFactor
		}

		if metadata.IsDefined("origins", k, "timeseries_eviction_method") {
			oc.TimeseriesEvictionMethodName = strings.ToLower(v.TimeseriesEvictionMethodName)
			if p, ok := timeseriesEvictionMethodNames[oc.TimeseriesEvictionMethodName]; ok {
				oc.TimeseriesEvictionMethod = p
			}
		}

		if metadata.IsDefined("origins", k, "fast_forward_disable") {
			oc.FastForwardDisable = v.FastForwardDisable
		}

		if metadata.IsDefined("origins", k, "backfill_tolerance_secs") {
			oc.BackfillToleranceSecs = v.BackfillToleranceSecs
		}

		if metadata.IsDefined("origins", k, "paths") {
			var j = 0
			for _, p := range v.Paths {
				p.Order = j
				p.ResponseBodyBytes = []byte(p.ResponseBody)

				// Set Default TTL?
				// Set Handler?
				oc.Paths[p.Path] = p
				j++
			}
		}

		if metadata.IsDefined("origins", k, "negative_cache") {
			oc.NegativeCacheSecs = v.NegativeCacheSecs
		}

		if metadata.IsDefined("origins", k, "health_check_endpoint") {
			oc.HealthCheckEndpoint = v.HealthCheckEndpoint
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

		c.Origins[k] = oc
	}
}

func (c *TricksterConfig) setCachingDefaults(metadata toml.MetaData) {

	// setCachingDefaults assumes that setOriginDefaults was just ran

	for k, v := range c.Caches {

		if _, ok := c.activeCaches[k]; !ok {
			// a configured cache was not used by any origin. don't even instantiate it
			delete(c.Caches, k)
			continue
		}

		cc := NewCacheConfig()

		if metadata.IsDefined("caches", k, "cache_type") {
			cc.CacheType = strings.ToLower(v.CacheType)
		}

		if metadata.IsDefined("caches", k, "compression") {
			cc.Compression = v.Compression
		}

		if metadata.IsDefined("caches", k, "timeseries_ttl_secs") {
			cc.TimeseriesTTLSecs = v.TimeseriesTTLSecs
		}

		if metadata.IsDefined("caches", k, "fastforward_ttl_secs") {
			cc.FastForwardTTLSecs = v.FastForwardTTLSecs
		}

		if metadata.IsDefined("caches", k, "max_object_size_bytes") {
			cc.MaxObjectSizeBytes = v.MaxObjectSizeBytes
		}

		if metadata.IsDefined("caches", k, "object_ttl_secs") {
			cc.ObjectTTLSecs = v.ObjectTTLSecs
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

		if cc.CacheType == "redis" {

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

	nc.Main.ConfigHandlerPath = c.Main.ConfigHandlerPath
	nc.Main.Environment = c.Main.Environment
	nc.Main.Hostname = c.Main.Hostname
	nc.Main.InstanceID = c.Main.InstanceID
	nc.Main.PingHandlerPath = c.Main.PingHandlerPath

	nc.Logging.LogFile = c.Logging.LogFile
	nc.Logging.LogLevel = c.Logging.LogLevel

	nc.Metrics.ListenAddress = c.Metrics.ListenAddress
	nc.Metrics.ListenPort = c.Metrics.ListenPort

	nc.ProxyServer.ListenAddress = c.ProxyServer.ListenAddress
	nc.ProxyServer.ListenPort = c.ProxyServer.ListenPort

	for k, v := range c.Origins {
		o := NewOriginConfig()
		o.BackfillTolerance = v.BackfillTolerance
		o.BackfillToleranceSecs = v.BackfillToleranceSecs
		o.CacheName = v.CacheName
		o.FastForwardDisable = v.FastForwardDisable
		o.Host = v.Host
		o.IsDefault = v.IsDefault
		o.KeepAliveTimeoutSecs = v.KeepAliveTimeoutSecs
		o.MaxIdleConns = v.MaxIdleConns
		o.PathPrefix = v.PathPrefix
		o.Scheme = v.Scheme
		o.Timeout = v.Timeout
		o.TimeoutSecs = v.TimeoutSecs
		o.OriginType = v.OriginType
		o.TimeseriesRetention = v.TimeseriesRetention
		o.TimeseriesRetentionFactor = v.TimeseriesRetentionFactor
		o.TimeseriesEvictionMethodName = v.TimeseriesEvictionMethodName
		o.TimeseriesEvictionMethod = v.TimeseriesEvictionMethod
		nc.Origins[k] = o
	}

	for k, v := range c.Caches {

		cc := NewCacheConfig()
		cc.Compression = v.Compression
		cc.FastForwardTTL = v.FastForwardTTL
		cc.FastForwardTTLSecs = v.FastForwardTTLSecs
		cc.ObjectTTL = v.ObjectTTL
		cc.ObjectTTLSecs = v.ObjectTTLSecs
		cc.TimeseriesTTL = v.TimeseriesTTL
		cc.TimeseriesTTLSecs = v.TimeseriesTTLSecs
		cc.CacheType = v.CacheType

		cc.Index.FlushInterval = v.Index.FlushInterval
		cc.Index.FlushIntervalSecs = v.Index.FlushIntervalSecs
		cc.Index.MaxSizeBackoffBytes = v.Index.MaxSizeBackoffBytes
		cc.Index.MaxSizeBackoffObjects = v.Index.MaxSizeBackoffObjects
		cc.Index.MaxSizeBytes = v.Index.MaxSizeBytes
		cc.Index.MaxSizeObjects = v.Index.MaxSizeObjects
		cc.Index.ReapInterval = v.Index.ReapInterval
		cc.Index.ReapIntervalSecs = v.Index.ReapIntervalSecs

		cc.Badger.Directory = v.Badger.Directory
		cc.Badger.ValueDirectory = v.Badger.ValueDirectory

		cc.Filesystem.CachePath = v.Filesystem.CachePath

		cc.BBolt.Bucket = v.BBolt.Bucket
		cc.BBolt.Filename = v.BBolt.Filename

		cc.Redis.ClientType = v.Redis.ClientType
		cc.Redis.DB = v.Redis.DB
		cc.Redis.DialTimeoutMS = v.Redis.DialTimeoutMS
		cc.Redis.Endpoint = v.Redis.Endpoint
		cc.Redis.Endpoints = v.Redis.Endpoints
		cc.Redis.IdleCheckFrequencyMS = v.Redis.IdleCheckFrequencyMS
		cc.Redis.IdleTimeoutMS = v.Redis.IdleTimeoutMS
		cc.Redis.MaxConnAgeMS = v.Redis.MaxConnAgeMS
		cc.Redis.MaxRetries = v.Redis.MaxRetries
		cc.Redis.MaxRetryBackoffMS = v.Redis.MaxRetryBackoffMS
		cc.Redis.MinIdleConns = v.Redis.MinIdleConns
		cc.Redis.MinRetryBackoffMS = v.Redis.MinRetryBackoffMS
		cc.Redis.Password = v.Redis.Password
		cc.Redis.PoolSize = v.Redis.PoolSize
		cc.Redis.PoolTimeoutMS = v.Redis.PoolTimeoutMS
		cc.Redis.Protocol = v.Redis.Protocol
		cc.Redis.ReadTimeoutMS = v.Redis.ReadTimeoutMS
		cc.Redis.SentinelMaster = v.Redis.SentinelMaster
		cc.Redis.WriteTimeoutMS = v.Redis.WriteTimeoutMS

		nc.Caches[k] = cc
	}

	return nc
}

func (c *TricksterConfig) String() string {
	cp := c.copy()
	for k, v := range cp.Caches {
		if v != nil {
			cp.Caches[k].Redis.Password = "*****"
		}
	}

	var buf bytes.Buffer
	e := toml.NewEncoder(&buf)
	e.Encode(cp)
	return buf.String()
}

// LookupPathConfig will return a ProxyPathConfig based on the provided path and lookup dictionary
func LookupPathConfig(dict map[string]*ProxyPathConfig, path string) *ProxyPathConfig {

	// Determine PathConfig
	var configuredPath string
	for p := range dict {
		if strings.HasPrefix(path, p) && len(p) > len(path) {
			configuredPath = p
		}
	}

	if configuredPath != "" {
		fmt.Println("Found path config")
		return dict[configuredPath]
	}

	return nil
}
