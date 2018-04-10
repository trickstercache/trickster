package main

import (
	"github.com/BurntSushi/toml"
)

// Config File Model for Application
type Config struct {
	Main             GeneralConfig                     `toml:"main"`
	Metrics          MetricsConfig                     `toml:"metrics"`
	Logging          LoggingConfig                     `toml:"logging"`
	Caching          CachingConfig                     `toml:"cache"`
	Origins          map[string]PrometheusOriginConfig `toml:"origins"`
	ProxyServer      ProxyServerConfig                 `toml:"proxy_server"`
	DefaultOriginURL string                            // to capture a CLI origin url
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
	// CacheType represents the type of cache that we wish to use: "memory", "filesystem", or "redis"
	CacheType     string                `toml:"cache_type"`
	RecordTTLSecs int64                 `toml:"record_ttl_secs"`
	Redis         RedisConfig           `toml:"redis"`
	Filesystem    FilesystemCacheConfig `toml:"filesystem"`
	ReapSleepMS   int64                 `toml:"reap_sleep_ms"`
	Compression   bool                  `toml:"compression"`
}

// RedisConfig is a collection of Configurations for Connecting to Redis
type RedisConfig struct {
	// Protocol represents the connection method (e.g., "tcp", "unix", etc.)
	Protocol string `toml:"protocol"`
	// Endpoint represents FQDN:port or IPAddress:Port of the Redis server
	Endpoint string `toml:"endpoint"`
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
	DefaultStep         int    `toml:"default_step"`
	IgnoreNoCacheHeader bool   `toml:"ignore_no_cache_header"`
	MaxValueAgeSecs     int64  `toml:"max_value_age_secs"`
	FastForwardDisable  bool   `toml:"fast_forward_disable"`
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
	return &Config{
		Main: GeneralConfig{
			ConfigFile: "/etc/trickster/trickster.conf",
			Hostname:   "localhost.unknown",
		},

		ProxyServer: ProxyServerConfig{
			ListenPort: 9090,
		},

		Origins: map[string]PrometheusOriginConfig{"default": defaultOriginConfig()},

		Metrics: MetricsConfig{
			ListenPort: 8082,
		},

		Logging: LoggingConfig{LogFile: "", LogLevel: "INFO"},

		Caching: CachingConfig{
			CacheType:     ctMemory,
			RecordTTLSecs: 21600,
			Redis:         RedisConfig{Protocol: "tcp", Endpoint: "redis:6379"},
			Filesystem:    FilesystemCacheConfig{CachePath: "/tmp"},
			ReapSleepMS:   1000,
			Compression:   true,
		},
	}
}

func defaultOriginConfig() PrometheusOriginConfig {

	return PrometheusOriginConfig{
		OriginURL:           "http://prometheus:9090/",
		APIPath:             "/api/v1/",
		DefaultStep:         300,
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400, // Keep datapoints up to 24 hours old
	}
}

// LoadFile loads application configuration from a TOML-formatted file.
func (c *Config) LoadFile(path string) error {
	_, err := toml.DecodeFile(path, &c)
	return err
}

//
