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
var defaultOriginURL string
var defaultOriginType string

// ApplicationName is the name of the Application
var ApplicationName string

// ApplicationVersion holds the version of the Application
var ApplicationVersion string

// TricksterConfig is the main configuration object
type TricksterConfig struct {
	Main        *MainConfig               `toml:"main"`
	Origins     map[string]*OriginConfig  `toml:"origins"`
	Caches      map[string]*CachingConfig `toml:"caches"`
	ProxyServer *ProxyServerConfig        `toml:"proxy_server"`
	Logging     *LoggingConfig            `toml:"logging"`
	Metrics     *MetricsConfig            `toml:"metrics"`
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `toml:"instance_id"`
	// Environment indicates the operating environment of the running instance (e.g., "dev", "stage", "prod")
	Environment string
	// Hostname is populated with the self-resolved Hostname where the instance is running
	Hostname string
}

// OriginConfig is a collection of configurations for prometheus origins proxied by Trickster
// You can override these on a per-request basis with url-params
type OriginConfig struct {
	Type                  string `toml:"type"`
	Scheme                string `toml:"scheme"`
	Host                  string `toml:"host"`
	PathPrefix            string `toml:"path_prefix"`
	APIPath               string `toml:"api_path"`
	IgnoreNoCacheHeader   bool   `toml:"ignore_no_cache_header"`
	ValueRetentionFactor  int    `toml:"value_retention_factor"`
	FastForwardDisable    bool   `toml:"fast_forward_disable"`
	BackfillToleranceSecs int64  `toml:"backfill_tolerance_secs"`
	TimeoutSecs           int64  `toml:"timeout_secs"`
	CacheName             string `toml:"cache_name"`

	Timeout           time.Duration `toml:"-"`
	BackfillTolerance time.Duration `toml:"-"`
	ValueRetention    time.Duration `toml:"-"`
}

// CachingConfig is a collection of defining the Trickster Caching Behavior
type CachingConfig struct {
	// Type represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	Type               string                `toml:"type"`
	Compression        bool                  `toml:"compression"`
	TimeseriesTTLSecs  int                   `toml:"timeseries_ttl_secs"`
	ObjectTTLSecs      int                   `toml:"object_ttl_secs"`
	FastForwardTTLSecs int                   `toml:"fastforward_ttl_secs"`
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

	defaultCachePath := "/tmp/trickster"
	defaultBBoltFile := "trickster.db"

	return &TricksterConfig{
		Caches: map[string]*CachingConfig{
			"default": &CachingConfig{
				Type:               "memory",
				Compression:        true,
				TimeseriesTTLSecs:  21600,
				FastForwardTTLSecs: 15,
				ObjectTTLSecs:      30,
				Redis:              RedisCacheConfig{ClientType: "standard", Protocol: "tcp", Endpoint: "redis:6379", Endpoints: []string{"redis:6379"}},
				Filesystem:         FilesystemCacheConfig{CachePath: defaultCachePath},
				BBolt:              BBoltCacheConfig{Filename: defaultBBoltFile, Bucket: "trickster"},
				Badger:             BadgerCacheConfig{Directory: defaultCachePath, ValueDirectory: defaultCachePath},
				Index: CacheIndexConfig{
					ReapIntervalSecs:      3,
					FlushIntervalSecs:     5,
					MaxSizeBytes:          536870912,
					MaxSizeBackoffBytes:   16777216,
					MaxSizeObjects:        0,
					MaxSizeBackoffObjects: 100,
				},
			},
		},
		Logging: &LoggingConfig{
			LogFile:  "",
			LogLevel: "INFO",
		},
		Main: &MainConfig{
			Hostname: "localhost.unknown",
		},
		Metrics: &MetricsConfig{
			ListenPort: 8082,
		},
		Origins: map[string]*OriginConfig{
			"default": DefaultOriginConfig(),
		},
		ProxyServer: &ProxyServerConfig{
			ListenPort: 9090,
		},
	}
}

func DefaultOriginConfig() *OriginConfig {
	return &OriginConfig{
		Type:                  "prometheus",
		Scheme:                "http",
		Host:                  "prometheus:9090",
		APIPath:               "/api/v1/",
		IgnoreNoCacheHeader:   true,
		ValueRetentionFactor:  1024, // Cache a max of 1024 recent timestamps of data for each query
		TimeoutSecs:           180,
		CacheName:             "default",
		ValueRetention:        1024,
		Timeout:               time.Second * 180,
		BackfillToleranceSecs: 0,
		BackfillTolerance:     0,
	}
}

// loadFile loads application configuration from a TOML-formatted file.
func (c *TricksterConfig) loadFile() error {
	_, err := toml.DecodeFile(Flags.ConfigPath, &c)
	return err
}
