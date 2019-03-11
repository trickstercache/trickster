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

import "github.com/BurntSushi/toml"

var Config *TricksterConfig
var Main *MainConfig
var Origins map[string]OriginConfig
var Caches map[string]CachingConfig
var ProxyServer *ProxyServerConfig
var Logging *LoggingConfig
var Metrics *MetricsConfig

var DefaultOriginURL string
var Flags = TricksterFlags{}

var ApplicationName string
var ApplicationVersion string

// Config is the main configuration object
type TricksterConfig struct {
	Main        MainConfig               `toml:"main"`
	Origins     map[string]OriginConfig  `toml:"origins"`
	Caches      map[string]CachingConfig `toml:"caches"`
	ProxyServer ProxyServerConfig        `toml:"proxy_server"`
	Logging     LoggingConfig            `toml:"logging"`
	Metrics     MetricsConfig            `toml:"metrics"`
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
	Type                string `toml:"type"`
	Scheme              string `toml:"scheme"`
	Host                string `toml:"host"`
	PathPrefix          string `toml:"path_prefix"`
	APIPath             string `toml:"api_path"`
	IgnoreNoCacheHeader bool   `toml:"ignore_no_cache_header"`
	MaxValueAgeSecs     int64  `toml:"max_value_age_secs"`
	FastForwardDisable  bool   `toml:"fast_forward_disable"`
	BackfillTolerance   int64  `toml:"backfill_tolerance"`
	TimeoutSecs         int64  `toml:"timeout_secs"`
	CacheName           string `toml:"cache_name"`
}

// CachingConfig is a collection of defining the Trickster Caching Behavior
type CachingConfig struct {
	// CacheType represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	Type           string                `toml:"cache_type"`
	RecordTTLSecs  int                   `toml:"record_ttl_secs"`
	Redis          RedisCacheConfig      `toml:"redis"`
	Filesystem     FilesystemCacheConfig `toml:"filesystem"`
	ReapIntervalMS int                   `toml:"reap_interval_ms"`
	Compression    bool                  `toml:"compression"`
	BBolt          BBoltCacheConfig      `toml:"boltdb"`
}

// RedisCacheConfig is a collection of Configurations for Connecting to Redis
type RedisCacheConfig struct {
	// Protocol represents the connection method (e.g., "tcp", "unix", etc.)
	Protocol string `toml:"protocol"`
	// Endpoint represents FQDN:port or IPAddress:Port of the Redis server
	Endpoint string `toml:"endpoint"`
	// Password can be set when using password protected redis instance.
	Password string `toml:"password"`
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
		Caches: map[string]CachingConfig{
			"default": {
				Type:           "memory",
				RecordTTLSecs:  21600,
				Redis:          RedisCacheConfig{Protocol: "tcp", Endpoint: "redis:6379"},
				Filesystem:     FilesystemCacheConfig{CachePath: defaultCachePath},
				BBolt:          BBoltCacheConfig{Filename: defaultBBoltFile, Bucket: "trickster"},
				ReapIntervalMS: 1000,
				Compression:    true,
			},
		},
		Logging: LoggingConfig{
			LogFile:  "",
			LogLevel: "INFO",
		},
		Main: MainConfig{
			Hostname: "localhost.unknown",
		},
		Metrics: MetricsConfig{
			ListenPort: 8082,
		},
		Origins: map[string]OriginConfig{
			"default": defaultOriginConfig(),
		},
		ProxyServer: ProxyServerConfig{
			ListenPort: 9090,
		},
	}
}

func defaultOriginConfig() OriginConfig {
	return OriginConfig{
		Type:                "prometheus",
		Scheme:              "http",
		Host:                "prometheus:9090",
		APIPath:             "/api/v1/",
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400, // Keep datapoints up to 24 hours old
		TimeoutSecs:         180,
		CacheName:           "default",
	}
}

// loadFile loads application configuration from a TOML-formatted file.
func (c *TricksterConfig) loadFile() error {
	_, err := toml.DecodeFile(Flags.ConfigPath, &c)
	return err
}
