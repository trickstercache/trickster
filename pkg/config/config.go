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
	"fmt"
	"os"
	"sync"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	cache "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	disco "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	yamlencoding "github.com/trickstercache/trickster/v2/pkg/encoding/yaml"
	fropt "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"

	"go.yaml.in/yaml/v3"
)

const defaultResourceName = "default"

// Config is the main configuration object
type Config struct {
	// Main is the primary MainConfig section
	Main *MainConfig `yaml:"main,omitempty"`
	// Backends is a map of BackendOptions
	Backends bo.Lookup `yaml:"backends,omitempty"`
	// Caches is a map of CacheConfigs
	Caches cache.Lookup `yaml:"caches,omitempty"`
	// Discovery is a map of named discoverer configurations for ALB pool
	// autodiscovery
	Discovery disco.Lookup `yaml:"discovery,omitempty"`
	// Frontend provides configurations about the Proxy Front End
	// Frontend is deprecated and will be phased out in a future release
	Frontend *fropt.Options `yaml:"frontend,omitempty"`
	// Listeners maps inbound listener names to their configurations.
	Listeners listener.Lookup `yaml:"listeners,omitempty"`
	// Logging provides configurations that affect logging behavior
	Logging *lo.Options `yaml:"logging,omitempty"`
	// Metrics provides configurations for collecting Metrics about the application
	Metrics *mo.Options `yaml:"metrics,omitempty"`
	// TracingOptions provides the distributed tracing configuration
	TracingOptions tracing.Lookup `yaml:"tracing,omitempty"`
	// NegativeCacheConfigs is a map of NegativeCacheConfigs
	NegativeCacheConfigs negative.ConfigLookup `yaml:"negative_caches,omitempty"`
	// Rules is a map of the Rules
	Rules rule.Lookup `yaml:"rules,omitempty"`
	// RequestRewriters is a map of the Rewriters
	RequestRewriters rwopts.Lookup `yaml:"request_rewriters,omitempty"`
	// MgmtConfig provides configurations for managing the trickster process
	// including reloading, purging cache entries, and health checks
	MgmtConfig *mgmt.Options `yaml:"mgmt,omitempty"`
	// Authenticators provides configurations for Authenticating users
	Authenticators auth.Lookup `yaml:"authenticators,omitempty"`

	// Flags contains a compiled version of the CLI flags
	Flags *Flags `yaml:"-"`
	// Resources holds runtime resources uses by the Config
	// Resources *Resources `yaml:"-"`

	CompiledRewriters      rewriter.InstructionsLookup `yaml:"-"`
	CompiledNegativeCaches negative.Lookups            `yaml:"-"`
	providedOriginURL      string
	providedProvider       string

	LoaderWarnings []string `yaml:"-"`

	listenerOverrides  map[string][]byte
	legacyFrontendUsed bool
	legacyMetricsUsed  bool
	legacyMgmtUsed     bool
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `yaml:"instance_id,omitempty"`
	// ServerName represents the server name that is conveyed in Via headers to upstream origins
	// defaults to os.Hostname
	ServerName string `yaml:"server_name,omitempty"`
	// ConfigIncludeDirectory optionally overrides the default sibling conf.d directory.
	ConfigIncludeDirectory string `yaml:"config_include_directory,omitempty"`

	configFilePath          string
	configSourcePlan        configSourcePlan
	configSourcePaths       []string
	configSourceFingerprint string
	configLastModified      time.Time
	configRateLimitTime     time.Time
	stalenessCheckLock      sync.Mutex
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *Config {
	hn, _ := os.Hostname()
	return &Config{
		Caches: cache.Lookup{
			defaultResourceName: cache.New(),
		},
		Logging: lo.New(),
		Main: &MainConfig{
			ServerName: hn,
		},
		MgmtConfig: mgmt.New(),
		Metrics:    mo.New(),
		Backends: bo.Lookup{
			defaultResourceName: bo.New(),
		},
		Frontend:  fropt.New(),
		Listeners: listener.NewLookup(),
		NegativeCacheConfigs: negative.ConfigLookup{
			defaultResourceName: negative.New(),
		},
		TracingOptions: tracing.Lookup{
			defaultResourceName: tracing.New(),
		},
		LoaderWarnings: make([]string, 0),
	}
}

// loadFile loads application configuration from a YAML-formatted file or directory.
func (c *Config) loadFile(flags *Flags) error {
	plan, sources, err := loadConfigSources(flags.ConfigPath)
	if err != nil {
		return err
	}
	configData := sources[0].data
	if plan.mode == configSourceModeDirectory || len(sources) > 1 {
		configData, err = mergeConfigSources(plan, sources)
		if err != nil {
			return err
		}
	} else if _, err = parseConfigDocument(configData); err != nil {
		return fmt.Errorf("parse config source %q: %w", sources[0].path, err)
	}
	if err := c.loadYAMLConfig(string(configData)); err != nil {
		return err
	}
	if c.Main == nil {
		c.Main = &MainConfig{}
	}
	snapshot := snapshotConfigSources(plan, sources, nil)
	c.Main.configFilePath = flags.ConfigPath
	c.Main.configSourcePlan = plan
	c.Main.configSourcePaths = make([]string, len(sources))
	for i, source := range sources {
		c.Main.configSourcePaths[i] = source.path
	}
	c.Main.configSourceFingerprint = snapshot.fingerprint
	c.Main.configLastModified = snapshot.lastModified
	return nil
}

// loadYAMLConfig loads application configuration from a YAML-formatted byte slice.
func (c *Config) loadYAMLConfig(yml string) error {
	if err := c.detectListenerSections(yml); err != nil {
		return err
	}
	err := yaml.Unmarshal([]byte(yml), &c)
	if err != nil {
		return err
	}

	if len(c.Backends) > 0 {
		err = c.Backends.Initialize()
		if err != nil {
			return err
		}
	}

	if len(c.Rules) > 0 {
		err = c.Rules.Initialize()
		if err != nil {
			return err
		}
	}

	return nil
}

// CheckFileLastModified returns the latest modification time among the running config sources.
func (c *Config) CheckFileLastModified() time.Time {
	if c.Main == nil || c.Main.configFilePath == "" {
		return time.Time{}
	}
	if c.Main.configSourcePlan.mode != 0 &&
		c.Main.configSourcePlan.rootPath == c.Main.configFilePath {
		return inspectConfigSources(c.Main.configSourcePlan).lastModified
	}
	file, err := os.Stat(c.Main.configFilePath)
	if err != nil {
		return time.Time{}
	}
	return file.ModTime()
}

// HasConfigChanged reports whether the configuration sources have changed since they were loaded.
// Unlike IsStale, it does not apply or update the reload rate limiter.
func (c *Config) HasConfigChanged() bool {
	if c == nil || c.Main == nil {
		return false
	}
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()
	return c.hasConfigChanged()
}

func (c *Config) hasConfigChanged() bool {
	if c.Main.configFilePath == "" {
		return false
	}
	if c.Main.configSourcePlan.mode != 0 &&
		c.Main.configSourcePlan.rootPath == c.Main.configFilePath {
		snapshot := inspectConfigSources(c.Main.configSourcePlan)
		return snapshot.fingerprint != c.Main.configSourceFingerprint
	}
	t := c.CheckFileLastModified()
	return !t.IsZero() && !t.Equal(c.Main.configLastModified)
}

// Process converts various raw config options into internal data structures
// as needed
func (c *Config) Process() error {
	var err error
	if c.RequestRewriters != nil {
		if c.CompiledRewriters,
			err = rewriter.ProcessConfigs(c.RequestRewriters); err != nil {
			return err
		}
		for _, b := range c.Backends {
			if b.ReqRewriterName != "" {
				ri, ok := c.CompiledRewriters[b.ReqRewriterName]
				if !ok {
					return bo.NewErrInvalidRewriterName(b.ReqRewriterName, b.Name)
				}
				b.ReqRewriter = ri
			}
			for _, p := range b.Paths {
				if p.ReqRewriterName != "" {
					ri, ok := c.CompiledRewriters[p.ReqRewriterName]
					if !ok {
						return fmt.Errorf("invalid rewriter name %s in path %s of backend options %s",
							p.ReqRewriterName, p.Path, b.Name)
					}
					p.ReqRewriter = ri
				}
			}
		}
	}
	tracing.ProcessTracingOptions(c.TracingOptions)
	return nil
}

// Clone returns an exact copy of the subject *Config
func (c *Config) Clone() *Config {
	nc := NewConfig()
	delete(nc.Caches, defaultResourceName)
	delete(nc.Backends, defaultResourceName)

	nc.Main.InstanceID = c.Main.InstanceID
	nc.Main.ServerName = c.Main.ServerName
	nc.Main.ConfigIncludeDirectory = c.Main.ConfigIncludeDirectory

	nc.MgmtConfig = c.MgmtConfig.Clone()
	nc.Listeners = c.Listeners.Clone()
	if len(c.listenerOverrides) > 0 {
		nc.listenerOverrides = make(map[string][]byte, len(c.listenerOverrides))
		for name, data := range c.listenerOverrides {
			nc.listenerOverrides[name] = append([]byte(nil), data...)
		}
	}
	nc.legacyFrontendUsed = c.legacyFrontendUsed
	nc.legacyMetricsUsed = c.legacyMetricsUsed
	nc.legacyMgmtUsed = c.legacyMgmtUsed

	c.Main.stalenessCheckLock.Lock()
	nc.Main.configFilePath = c.Main.configFilePath
	nc.Main.configSourcePlan = c.Main.configSourcePlan
	nc.Main.configSourcePaths = append([]string(nil), c.Main.configSourcePaths...)
	nc.Main.configSourceFingerprint = c.Main.configSourceFingerprint
	nc.Main.configLastModified = c.Main.configLastModified
	nc.Main.configRateLimitTime = c.Main.configRateLimitTime
	c.Main.stalenessCheckLock.Unlock()

	nc.Metrics.ListenAddress = c.Metrics.ListenAddress
	nc.Metrics.ListenPort = c.Metrics.ListenPort

	if c.Frontend != nil {
		nc.Frontend = c.Frontend.Clone()
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

	if len(c.Discovery) > 0 {
		nc.Discovery = c.Discovery.Clone()
	}

	for k, v := range c.NegativeCacheConfigs {
		nc.NegativeCacheConfigs[k] = v.Clone()
	}

	for k, v := range c.TracingOptions {
		nc.TracingOptions[k] = v.Clone()
	}

	if len(c.Rules) > 0 {
		nc.Rules = make(rule.Lookup, len(c.Rules))
		for k, v := range c.Rules {
			nc.Rules[k] = v.Clone()
		}
	}

	if len(c.RequestRewriters) > 0 {
		nc.RequestRewriters = make(rwopts.Lookup, len(c.RequestRewriters))
		for k, v := range c.RequestRewriters {
			nc.RequestRewriters[k] = v.Clone()
		}
	}

	if len(c.Authenticators) > 0 {
		nc.Authenticators = make(auth.Lookup, len(c.Authenticators))
		for k, v := range c.Authenticators {
			nc.Authenticators[k] = v.Clone()
		}
	}

	return nc
}

// IsStale returns true if the running config is stale versus its sources on disk.
func (c *Config) IsStale() bool {
	if c == nil || c.Main == nil {
		return false
	}
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()

	if c.Main.configFilePath == "" ||
		time.Now().Before(c.Main.configRateLimitTime) {
		return false
	}

	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}

	c.Main.configRateLimitTime = time.Now().Add(time.Duration(c.MgmtConfig.ReloadRateLimit))
	return c.hasConfigChanged()
}

// CheckAndMarkReloadInProgress checks if the config is stale and
// marks it as being reloaded to prevent duplicate reloads.
func (c *Config) CheckAndMarkReloadInProgress() bool {
	if c == nil || c.Main == nil || c.Main.configFilePath == "" {
		return false
	}
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()
	if time.Now().Before(c.Main.configRateLimitTime) {
		return false
	}
	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}
	c.Main.configRateLimitTime = time.Now().Add(time.Duration(c.MgmtConfig.ReloadRateLimit))
	if c.Main.configSourcePlan.mode != 0 &&
		c.Main.configSourcePlan.rootPath == c.Main.configFilePath {
		snapshot := inspectConfigSources(c.Main.configSourcePlan)
		isStale := snapshot.fingerprint != c.Main.configSourceFingerprint
		if isStale {
			c.Main.configSourceFingerprint = snapshot.fingerprint
			c.Main.configLastModified = snapshot.lastModified
		}
		return isStale
	}
	t := c.CheckFileLastModified()
	if t.IsZero() {
		return false
	}
	isStale := !t.Equal(c.Main.configLastModified)
	if isStale {
		c.Main.configLastModified = t
	}
	return isStale
}

func (c *Config) String() string {
	cp := c.Clone()

	for k, o := range cp.Backends {
		cp.Backends[k] = o.CloneYAMLSafe()
	}

	for k, o := range cp.Authenticators {
		cp.Authenticators[k] = o.CloneYAMLSafe()
	}

	// strip Redis password
	for k, v := range cp.Caches {
		if v != nil && cp.Caches[k].Redis != nil && cp.Caches[k].Redis.Password != "" {
			cp.Caches[k].Redis.Password = "*****"
		}
	}

	bytes, err := yamlencoding.Marshal(cp)
	if err == nil {
		return string(bytes)
	}

	return ""
}

// ConfigFilePath returns the file or directory path from which this configuration is based.
func (c *Config) ConfigFilePath() string {
	if c.Main != nil {
		return c.Main.configFilePath
	}
	return ""
}

// ConfigFilePaths returns the configuration files in application order.
func (c *Config) ConfigFilePaths() []string {
	if c == nil || c.Main == nil {
		return nil
	}
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()
	if len(c.Main.configSourcePaths) > 0 {
		return append([]string(nil), c.Main.configSourcePaths...)
	}
	if c.Main.configFilePath != "" {
		return []string{c.Main.configFilePath}
	}
	return nil
}
