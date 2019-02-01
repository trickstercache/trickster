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

package main

import "github.com/BurntSushi/toml"

// Config is the main configuration object
type Config struct {
	Caching          CachingConfig                     `toml:"cache"`
	DefaultOriginURL string                            // to capture a CLI origin url
	Logging          LoggingConfig                     `toml:"logging"`
	Main             GeneralConfig                     `toml:"main"`
	Metrics          MetricsConfig                     `toml:"metrics"`
	Origins          map[string]PrometheusOriginConfig `toml:"origins"`
	ProxyServer      ProxyServerConfig                 `toml:"proxy_server"`
}

// GeneralConfig is a collection of general configuration values.
type GeneralConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `toml:"instance_id"`
	// Environment indicates the operating environment of the running instance (e.g., "dev", "stage", "prod")
	Environment string
	// ConfigFile represents the physical filepath to the Trickster Configuration
	ConfigFile string
	// Hostname is populated with the self-resolved Hostname where the instance is running
	Hostname string
}

// ProxyServerConfig is a collection of configurations for the main http listener for the application
type ProxyServerConfig struct {
	// ListenAddress is IP address for the main http listener for the application
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port for the main http listener for the application
	ListenPort int `toml:"listen_port"`
}

// CachingConfig is a collection of defining the Trickster Caching Behavior
type CachingConfig struct {
	// CacheType represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	CacheType     string                `toml:"cache_type"`
	RecordTTLSecs int64                 `toml:"record_ttl_secs"`
	Redis         RedisCacheConfig      `toml:"redis"`
	Filesystem    FilesystemCacheConfig `toml:"filesystem"`
	ReapSleepMS   int64                 `toml:"reap_sleep_ms"`
	Compression   bool                  `toml:"compression"`
	BoltDB        BoltDBCacheConfig     `toml:"boltdb"`
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

// BoltDBCacheConfig is a collection of Configurations for storing cached data on the Filesystem
type BoltDBCacheConfig struct {
	// Filename represents the filename (including path) of the BotlDB database
	Filename string `toml:"filename"`
	// Bucket represents the name of the bucket within BoltDB under which Trickster's keys will be stored.
	Bucket string `toml:"bucket"`
}

// FilesystemCacheConfig is a collection of Configurations for storing cached data on the Filesystem
type FilesystemCacheConfig struct {
	// CachePath represents the path on disk where our cache will live
	CachePath string `toml:"cache_path"`
}

// PrometheusOriginConfig is a collection of configurations for prometheus origins proxied by Trickster
// You can override these on a per-request basis with url-params
type PrometheusOriginConfig struct {
	OriginURL           string `toml:"origin_url"`
	APIPath             string `toml:"api_path"`
	IgnoreNoCacheHeader bool   `toml:"ignore_no_cache_header"`
	MaxValueAgeSecs     int64  `toml:"max_value_age_secs"`
	FastForwardDisable  bool   `toml:"fast_forward_disable"`
	NoCacheLastDataSecs int64  `toml:"no_cache_last_data_secs"`
}

// MetricsConfig is a collection of Metrics Collection configurations
type MetricsConfig struct {
	// ListenAddress is IP address from which the Application Metrics are available for pulling at /metrics
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port from which the Application Metrics are available for pulling at /metrics
	ListenPort int `toml:"listen_port"`
}

// LoggingConfig is a collection of Logging configurations
type LoggingConfig struct {
	// LogFile provides the filepath to the instances's logfile. Set as empty string to Log to Console
	LogFile string `toml:"log_file"`
	// LogLevel provides the most granular level (e.g., DEBUG, INFO, ERROR) to log
	LogLevel string `toml:"log_level"`
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *Config {

	defaultCachePath := "/tmp/trickster"
	defaultBoltDBFile := "trickster.db"

	return &Config{
		Caching: CachingConfig{

			CacheType:     ctMemory,
			RecordTTLSecs: 21600,

			Redis:      RedisCacheConfig{Protocol: "tcp", Endpoint: "redis:6379"},
			Filesystem: FilesystemCacheConfig{CachePath: defaultCachePath},
			BoltDB:     BoltDBCacheConfig{Filename: defaultBoltDBFile, Bucket: "trickster"},

			ReapSleepMS: 1000,
			Compression: true,
		},
		Logging: LoggingConfig{
			LogFile:  "",
			LogLevel: "INFO",
		},
		Main: GeneralConfig{
			ConfigFile: "/etc/trickster/trickster.conf",
			Hostname:   "localhost.unknown",
		},
		Metrics: MetricsConfig{
			ListenPort: 8082,
		},
		Origins: map[string]PrometheusOriginConfig{
			"default": defaultOriginConfig(),
		},
		ProxyServer: ProxyServerConfig{
			ListenPort: 9090,
		},
	}
}

func defaultOriginConfig() PrometheusOriginConfig {
	return PrometheusOriginConfig{
		OriginURL:           "http://prometheus:9090/",
		APIPath:             prometheusAPIv1Path,
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400, // Keep datapoints up to 24 hours old
	}
}

// LoadFile loads application configuration from a TOML-formatted file.
func (c *Config) LoadFile(path string) error {
	_, err := toml.DecodeFile(path, &c)
	return err
}
