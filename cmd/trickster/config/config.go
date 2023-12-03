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

// Package config provides Trickster configuration abilities, including
// parsing and printing configuration files, command line parameters, and
// environment variables, as well as default values and state.
package config

import (
	"errors"
	"os"
	"sync"
	"time"

	reload "github.com/trickstercache/trickster/v2/cmd/trickster/config/reload/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	cache "github.com/trickstercache/trickster/v2/pkg/cache/options"
	fropt "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	rewriter "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

// Config is the main configuration object
type Config struct {
	// Main is the primary MainConfig section
	Main *MainConfig `yaml:"main,omitempty"`
	// Backends is a map of BackendOptionss
	Backends map[string]*bo.Options `yaml:"backends,omitempty"`
	// Caches is a map of CacheConfigs
	Caches map[string]*cache.Options `yaml:"caches,omitempty"`
	// ProxyServer is provides configurations about the Proxy Front End
	Frontend *fropt.Options `yaml:"frontend,omitempty"`
	// Logging provides configurations that affect logging behavior
	Logging *lo.Options `yaml:"logging,omitempty"`
	// Metrics provides configurations for collecting Metrics about the application
	Metrics *mo.Options `yaml:"metrics,omitempty"`
	// TracingConfigs provides the distributed tracing configuration
	TracingConfigs map[string]*tracing.Options `yaml:"tracing,omitempty"`
	// NegativeCacheConfigs is a map of NegativeCacheConfigs
	NegativeCacheConfigs map[string]negative.Config `yaml:"negative_caches,omitempty"`
	// Rules is a map of the Rules
	Rules map[string]*rule.Options `yaml:"rules,omitempty"`
	// RequestRewriters is a map of the Rewriters
	RequestRewriters map[string]*rwopts.Options `yaml:"request_rewriters,omitempty"`
	// ReloadConfig provides configurations for in-process config reloading
	ReloadConfig *reload.Options `yaml:"reloading,omitempty"`

	// Resources holds runtime resources uses by the Config
	Resources *Resources `yaml:"-"`

	CompiledRewriters map[string]rewriter.RewriteInstructions `yaml:"-"`
	activeCaches      map[string]interface{}
	providedOriginURL string
	providedProvider  string

	LoaderWarnings []string `yaml:"-"`
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `yaml:"instance_id,omitempty"`
	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `yaml:"config_handler_path,omitempty"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `yaml:"ping_handler_path,omitempty"`
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	ReloadHandlerPath string `yaml:"reload_handler_path,omitempty"`
	// HealthHandlerPath provides the base Health Check Handler path
	HealthHandlerPath string `yaml:"health_handler_path,omitempty"`
	// PurgeKeyHandlerPath provides the base Cache Purge Key Handler path
	PurgeKeyHandlerPath  string `yaml:"purge_key_handler_path,omitempty"`
	PurgePathHandlerPath string `yaml:"purge_path_handler_path,omitempty"`
	// PprofServer provides the name of the http listener that will host the pprof debugging routes
	// Options are: "metrics", "reload", "both", or "off"; default is both
	PprofServer string `yaml:"pprof_server,omitempty"`
	// ServerName represents the server name that is conveyed in Via headers to upstream origins
	// defaults to os.Hostname
	ServerName string `yaml:"server_name,omitempty"`

	// ReloaderLock is used to lock the config for reloading
	ReloaderLock sync.Mutex `yaml:"-"`

	configFilePath      string
	configLastModified  time.Time
	configRateLimitTime time.Time
	stalenessCheckLock  sync.Mutex
}

func (mc *MainConfig) SetStalenessInfo(fp string, lm, rlt time.Time) {
	mc.configFilePath = fp
	mc.configLastModified = lm
	mc.configRateLimitTime = rlt
}

// Resources is a collection of values used by configs at runtime that are not part of the config itself
type Resources struct {
	QuitChan chan bool `yaml:"-"`
	metadata yamlx.KeyLookup
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *Config {
	hn, _ := os.Hostname()
	return &Config{
		Caches: map[string]*cache.Options{
			"default": cache.New(),
		},
		Logging: lo.New(),
		Main: &MainConfig{
			ConfigHandlerPath:    DefaultConfigHandlerPath,
			PingHandlerPath:      DefaultPingHandlerPath,
			ReloadHandlerPath:    reload.DefaultReloadHandlerPath,
			HealthHandlerPath:    DefaultHealthHandlerPath,
			PurgeKeyHandlerPath:  DefaultPurgeKeyHandlerPath,
			PurgePathHandlerPath: DefaultPurgePathHandlerPath,
			PprofServer:          DefaultPprofServerName,
			ServerName:           hn,
		},
		Metrics: mo.New(),
		Backends: map[string]*bo.Options{
			"default": bo.New(),
		},
		Frontend: fropt.New(),
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

// loadFile loads application configuration from a YAML-formatted file.
func (c *Config) loadFile(flags *Flags) error {
	b, err := os.ReadFile(flags.ConfigPath)
	if err != nil {
		c.setDefaults(yamlx.KeyLookup{})
		return err
	}
	return c.loadYAMLConfig(string(b), flags)
}

// loadYAMLConfig loads application configuration from a YAML-formatted byte slice.
func (c *Config) loadYAMLConfig(yml string, flags *Flags) error {

	err := yaml.Unmarshal([]byte(yml), &c)
	if err != nil {
		return err
	}
	md, err := yamlx.GetKeyList(yml)
	if err != nil {
		c.setDefaults(yamlx.KeyLookup{})
		return err
	}
	err = c.setDefaults(md)
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

func (c *Config) setDefaults(metadata yamlx.KeyLookup) error {

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

	c.activeCaches = make(map[string]interface{})
	for k, v := range c.Backends {
		w, err := bo.SetDefaults(k, v, metadata, c.CompiledRewriters, c.Backends, c.activeCaches)
		if err != nil {
			return err
		}
		c.Backends[k] = w
	}

	tracing.ProcessTracingOptions(c.TracingConfigs, metadata)

	var lw []string
	if lw, err = cache.Lookup(c.Caches).SetDefaults(metadata, c.activeCaches); err != nil {
		return err
	}
	c.LoaderWarnings = append(c.LoaderWarnings, lw...)

	// This ensures that in places where backend options reference other named config sections
	// (like caches, rules, negative caches, tracing, etc) referenced by names, the names
	// referenced in the configuration are valid and refer to a defined resource
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
		c.Main.PprofServer = DefaultPprofServerName
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
	nc.Main.PurgeKeyHandlerPath = c.Main.PurgeKeyHandlerPath
	nc.Main.PurgePathHandlerPath = c.Main.PurgePathHandlerPath
	nc.Main.PprofServer = c.Main.PprofServer
	nc.Main.ServerName = c.Main.ServerName

	nc.Main.configFilePath = c.Main.configFilePath
	nc.Main.configLastModified = c.Main.configLastModified
	nc.Main.configRateLimitTime = c.Main.configRateLimitTime

	nc.Metrics.ListenAddress = c.Metrics.ListenAddress
	nc.Metrics.ListenPort = c.Metrics.ListenPort

	if c.Frontend != nil {
		nc.Frontend = c.Frontend.Clone()
	}

	nc.Resources = &Resources{
		QuitChan: make(chan bool, 1),
	}

	if c.Logging != nil {
		nc.Logging = c.Logging.Clone()
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

	for k, o := range cp.Backends {
		cp.Backends[k] = o.CloneYAMLSafe()
	}

	// strip Redis password
	for k, v := range cp.Caches {
		if v != nil && cp.Caches[k].Redis.Password != "" {
			cp.Caches[k].Redis.Password = "*****"
		}
	}

	bytes, err := yaml.Marshal(cp)
	if err == nil {
		return string(bytes)
	}

	return ""

}

// ConfigFilePath returns the file path from which this configuration is based
func (c *Config) ConfigFilePath() string {
	if c.Main != nil {
		return c.Main.configFilePath
	}
	return ""
}
