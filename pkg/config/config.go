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
	"errors"
	"io/ioutil"
	"os"
	"sync"
	"time"

	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
	rule "github.com/tricksterproxy/trickster/pkg/backends/rule/options"
	"github.com/tricksterproxy/trickster/pkg/cache/negative"
	cache "github.com/tricksterproxy/trickster/pkg/cache/options"
	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
	reload "github.com/tricksterproxy/trickster/pkg/config/reload/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	rewriter "github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter"
	rwopts "github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter/options"
	tracing "github.com/tricksterproxy/trickster/pkg/tracing/options"

	"github.com/BurntSushi/toml"
)

// Config is the main configuration object
type Config struct {
	// Main is the primary MainConfig section
	Main *MainConfig `toml:"main"`
	// Backends is a map of BackendOptionss
	Backends map[string]*bo.Options `toml:"backends"`
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
	NegativeCacheConfigs map[string]negative.Config `toml:"negative_caches"`
	// Rules is a map of the Rules
	Rules map[string]*rule.Options `toml:"rules"`
	// RequestRewriters is a map of the Rewriters
	RequestRewriters map[string]*rwopts.Options `toml:"request_rewriters"`
	// ReloadConfig provides configurations for in-process config reloading
	ReloadConfig *reload.Options `toml:"reloading"`

	// Resources holds runtime resources uses by the Config
	Resources *Resources `toml:"-"`

	CompiledRewriters map[string]rewriter.RewriteInstructions `toml:"-"`
	activeCaches      map[string]bool
	providedOriginURL string
	providedProvider  string

	LoaderWarnings []string `toml:"-"`
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `toml:"instance_id"`
	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `toml:"config_handler_path"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `toml:"ping_handler_path"`
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	ReloadHandlerPath string `toml:"reload_handler_path"`
	// HeatlHandlerPath provides the base Health Check Handler path
	HealthHandlerPath string `toml:"health_handler_path"`
	// PprofServer provides the name of the http listener that will host the pprof debugging routes
	// Options are: "metrics", "reload", "both", or "off"; default is both
	PprofServer string `toml:"pprof_server"`
	// ServerName represents the server name that is conveyed in Via headers to upstream origins
	// defaults to os.Hostname
	ServerName string `toml:"server_name"`

	// ReloaderLock is used to lock the config for reloading
	ReloaderLock sync.Mutex `toml:"-"`

	configFilePath      string
	configLastModified  time.Time
	configRateLimitTime time.Time
	stalenessCheckLock  sync.Mutex
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
	// at least one backend configuration has a valid certificate and key file configured.
	ServeTLS bool `toml:"-"`
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

// Resources is a collection of values used by configs at runtime that are not part of the config itself
type Resources struct {
	QuitChan chan bool `toml:"-"`
	metadata *toml.MetaData
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *Config {
	hn, _ := os.Hostname()
	return &Config{
		Caches: map[string]*cache.Options{
			"default": cache.New(),
		},
		Logging: &LoggingConfig{
			LogFile:  d.DefaultLogFile,
			LogLevel: d.DefaultLogLevel,
		},
		Main: &MainConfig{
			ConfigHandlerPath: d.DefaultConfigHandlerPath,
			PingHandlerPath:   d.DefaultPingHandlerPath,
			ReloadHandlerPath: d.DefaultReloadHandlerPath,
			HealthHandlerPath: d.DefaultHealthHandlerPath,
			PprofServer:       d.DefaultPprofServerName,
			ServerName:        hn,
		},
		Metrics: &MetricsConfig{
			ListenPort: d.DefaultMetricsListenPort,
		},
		Backends: map[string]*bo.Options{
			"default": bo.New(),
		},
		Frontend: &FrontendConfig{
			ListenPort:       d.DefaultProxyListenPort,
			ListenAddress:    d.DefaultProxyListenAddress,
			TLSListenPort:    d.DefaultTLSProxyListenPort,
			TLSListenAddress: d.DefaultTLSProxyListenAddress,
		},
		NegativeCacheConfigs: map[string]negative.Config{
			"default": negative.New(),
		},
		TracingConfigs: map[string]*tracing.Options{
			"default": tracing.New(),
		},
		ReloadConfig:   reload.New(),
		LoaderWarnings: make([]string, 0),
		Resources: &Resources{
			QuitChan: make(chan bool, 1),
		},
	}
}

// loadFile loads application configuration from a TOML-formatted file.
func (c *Config) loadFile(flags *Flags) error {
	b, err := ioutil.ReadFile(flags.ConfigPath)
	if err != nil {
		c.setDefaults(&toml.MetaData{})
		return err
	}
	return c.loadTOMLConfig(string(b), flags)
}

// loadTOMLConfig loads application configuration from a TOML-formatted byte slice.
func (c *Config) loadTOMLConfig(tml string, flags *Flags) error {
	md, err := toml.Decode(tml, c)
	if err != nil {
		c.setDefaults(&toml.MetaData{})
		return err
	}
	err = c.setDefaults(&md)
	if err == nil {
		c.Main.configFilePath = flags.ConfigPath
		c.Main.configLastModified = c.CheckFileLastModified()
	}
	return err
}

// CheckFileLastModified returns the last modified date of the running config file, if present
func (c *Config) CheckFileLastModified() time.Time {
	if c.Main == nil || c.Main.configFilePath == "" {
		return time.Time{}
	}
	file, err := os.Stat(c.Main.configFilePath)
	if err != nil {
		return time.Time{}
	}
	return file.ModTime()
}

func (c *Config) setDefaults(metadata *toml.MetaData) error {

	c.Resources.metadata = metadata

	var err error

	if err = c.processPprofConfig(); err != nil {
		return err
	}

	if c.RequestRewriters != nil {
		if c.CompiledRewriters, err = rewriter.ProcessConfigs(c.RequestRewriters); err != nil {
			return err
		}
	}

	c.activeCaches = make(map[string]bool)
	for k, v := range c.Backends {
		w, err := bo.ProcessTOML(k, v, metadata, c.CompiledRewriters, c.Backends, c.activeCaches)
		if err != nil {
			return err
		}
		c.Backends[k] = w
	}

	tracing.ProcessTracingOptions(c.TracingConfigs, metadata)

	var lw []string
	if lw, err = cache.Lookup(c.Caches).ProcessTOML(metadata, c.activeCaches); err != nil {
		return err
	}
	for _, v := range lw {
		c.LoaderWarnings = append(c.LoaderWarnings, v)
	}

	ol := bo.Lookup(c.Backends)
	if err = ol.ValidateConfigMappings(c.Rules, c.Caches); err != nil {
		return err
	}

	serveTLS, err := ol.ValidateTLSConfigs()
	if err != nil {
		return err
	}
	if serveTLS {
		c.Frontend.ServeTLS = true
	}
	return nil
}

// ErrInvalidPprofServerName returns an error for invalid pprof server name
var ErrInvalidPprofServerName = errors.New("invalid pprof server name")

func (c *Config) processPprofConfig() error {
	switch c.Main.PprofServer {
	case "metrics", "reload", "off", "both":
		return nil
	case "":
		c.Main.PprofServer = d.DefaultPprofServerName
		return nil
	}
	return ErrInvalidPprofServerName
}

// Clone returns an exact copy of the subject *Config
func (c *Config) Clone() *Config {

	nc := NewConfig()
	delete(nc.Caches, "default")
	delete(nc.Backends, "default")

	nc.Main.ConfigHandlerPath = c.Main.ConfigHandlerPath
	nc.Main.InstanceID = c.Main.InstanceID
	nc.Main.PingHandlerPath = c.Main.PingHandlerPath
	nc.Main.ReloadHandlerPath = c.Main.ReloadHandlerPath
	nc.Main.HealthHandlerPath = c.Main.HealthHandlerPath
	nc.Main.PprofServer = c.Main.PprofServer
	nc.Main.ServerName = c.Main.ServerName

	nc.Main.configFilePath = c.Main.configFilePath
	nc.Main.configLastModified = c.Main.configLastModified
	nc.Main.configRateLimitTime = c.Main.configRateLimitTime

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

	nc.Resources = &Resources{
		QuitChan: make(chan bool, 1),
	}

	for k, v := range c.Backends {
		nc.Backends[k] = v.Clone()
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

	if c.Rules != nil && len(c.Rules) > 0 {
		nc.Rules = make(map[string]*rule.Options)
		for k, v := range c.Rules {
			nc.Rules[k] = v.Clone()
		}
	}

	if c.RequestRewriters != nil && len(c.RequestRewriters) > 0 {
		nc.RequestRewriters = make(map[string]*rwopts.Options)
		for k, v := range c.RequestRewriters {
			nc.RequestRewriters[k] = v.Clone()
		}
	}

	return nc
}

// IsStale returns true if the running config is stale versus the
func (c *Config) IsStale() bool {

	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()

	if c.Main == nil || c.Main.configFilePath == "" ||
		time.Now().Before(c.Main.configRateLimitTime) {
		return false
	}

	if c.ReloadConfig == nil {
		c.ReloadConfig = reload.New()
	}

	c.Main.configRateLimitTime =
		time.Now().Add(time.Millisecond * time.Duration(c.ReloadConfig.RateLimitMS))
	t := c.CheckFileLastModified()
	if t.IsZero() {
		return false
	}
	return t != c.Main.configLastModified
}

func (c *Config) String() string {
	cp := c.Clone()

	// the toml library will panic if the Handler is assigned,
	// even though this field is annotated as skip ("-") in the prototype
	// so we'll iterate the paths and set to nil the Handler (in our local copy only)
	if cp.Backends != nil {
		for _, v := range cp.Backends {
			if v != nil {
				for _, w := range v.Paths {
					w.Handler = nil
					w.KeyHasher = nil
				}
			}
			if v.HealthCheck != nil {
				// also strip out potentially sensitive headers
				hideAuthorizationCredentials(v.HealthCheck.Headers)
			}
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

// ConfigFilePath returns the file path from which this configuration is based
func (c *Config) ConfigFilePath() string {
	if c.Main != nil {
		return c.Main.configFilePath
	}
	return ""
}

// Equal returns true if the FrontendConfigs are identical in value.
func (fc *FrontendConfig) Equal(fc2 *FrontendConfig) bool {
	return *fc == *fc2
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
