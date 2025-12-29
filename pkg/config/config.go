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
	"io/fs"
	"sync"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	cache "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	fropt "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"gopkg.in/yaml.v2"
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
	// Frontend provides configurations about the Proxy Front End
	Frontend *fropt.Options `yaml:"frontend,omitempty"`
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
}

// MainConfig is a collection of general configuration values.
type MainConfig struct {
	// InstanceID represents a unique ID for the current instance, when multiple instances on the same host
	InstanceID int `yaml:"instance_id,omitempty"`
	// ServerName represents the server name that is conveyed in Via headers to upstream origins
	// defaults to os.Hostname
	ServerName string `yaml:"server_name,omitempty"`

	configFilePath      string
	configFilesPath     []string
	configLastModified  time.Time
	configFilesLastMod  []time.Time
	configRateLimitTime time.Time
	stalenessCheckLock  sync.Mutex
}

func (mc *MainConfig) SetStalenessInfo(fp string, lm, rlt time.Time) {
	mc.stalenessCheckLock.Lock()
	mc.configFilePath = fp
	mc.configLastModified = lm
	mc.configRateLimitTime = rlt
	mc.stalenessCheckLock.Unlock()
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
			configFilesPath: make([]string, 0),
			configFilesLastMod: make([]time.Time, 0),
		},
		MgmtConfig: mgmt.New(),
		Metrics:    mo.New(),
		Backends: bo.Lookup{
			defaultResourceName: bo.New(),
		},
		Frontend: fropt.New(),
		NegativeCacheConfigs: negative.ConfigLookup{
			defaultResourceName: negative.New(),
		},
		TracingOptions: tracing.Lookup{
			defaultResourceName: tracing.New(),
		},
		LoaderWarnings: make([]string, 0),
	}
}

func (c *Config) loadConfigs(flags *Flags) error {
	ok, err := c.isDir(flags)
	if err != nil {
		return err
	}

	if !ok {
		return c.loadFile(flags)
	}
	return c.loadAndMergeFiles(flags)
}

// loadFile loads application configuration from a YAML-formatted file.
func (c *Config) loadFile(flags *Flags) error {
	b, err := os.ReadFile(flags.ConfigPath)
	if err != nil {
		return err
	}
	err = c.loadYAMLConfig(string(b))
	if err != nil {
		return err
	}
	c.Main.configFilePath = flags.ConfigPath
	c.Main.configLastModified = c.CheckFileLastModified("")
	return nil
}

// loadAndMergeFiles loads application configuration from multiple YAML files
func (c *Config) loadAndMergeFiles(flags *Flags) error {
	files, err := fs.Glob(os.DirFS(flags.ConfigPath), "*.yaml")
	if err != nil {
		return err
	}

	for _, file := range files {
		path := flags.ConfigPath + "/" + file
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if err = c.loadYAMLConfig(string(data)); err != nil {
			return err
		}

		c.Main.configFilesPath = append(
			c.Main.configFilesPath,
			path,
		)
		c.Main.configFilesLastMod = append(
			c.Main.configFilesLastMod,
			c.CheckFileLastModified(path),
		)
	}
	return nil
}

func (c *Config) isDir(flags *Flags) (bool, error) {
	finfo, err := os.Stat(flags.ConfigPath)
	if err != nil {
		return false, err
	}

	return finfo.IsDir(), nil
}

// loadYAMLConfig loads application configuration from a YAML-formatted byte slice.
func (c *Config) loadYAMLConfig(yml string) error {
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

// CheckFileLastModified returns the last modified date of the running config file, if present
func (c *Config) CheckFileLastModified(confFile string) time.Time {
	if c.Main == nil {
		return time.Time{}
	}

	path := confFile
	if path == "" {
		path = c.Main.configFilePath
		if path == "" {
			return time.Time{}
		}
	}

	fInfo, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fInfo.ModTime()
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

	nc.MgmtConfig = c.MgmtConfig.Clone()

	nc.Main.configFilePath = c.Main.configFilePath
	nc.Main.configFilesPath = c.Main.configFilesPath
	nc.Main.configLastModified = c.Main.configLastModified
	nc.Main.configFilesLastMod = c.Main.configFilesLastMod
	nc.Main.configRateLimitTime = c.Main.configRateLimitTime

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

// IsStale returns true if the running config is stale versus the config on disk
func (c *Config) IsStale() bool {
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()

	if c.Main == nil ||
   		(len(c.Main.configFilesPath) == 0 &&
    		(c.Main.configFilePath == "" ||
     			time.Now().Before(c.Main.configRateLimitTime))) {
    	return false
	}	

	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}

	c.Main.configRateLimitTime = time.Now().Add(c.MgmtConfig.ReloadRateLimit)
	if len(c.Main.configFilesPath) > 0 {
		for index, file := range c.Main.configFilesPath {
			t := c.CheckFileLastModified(file)
			if t.IsZero() {
				continue
			}
			if !t.Equal(c.Main.configFilesLastMod[index]) {
				return true
			} 
		}
		return false
	}
	
	t := c.CheckFileLastModified("")
	if t.IsZero() {
		return false
	}
	return !t.Equal(c.Main.configLastModified)
}

// CheckAndMarkReloadInProgress checks if the config is stale and
// marks it as being reloaded to prevent duplicate reloads.
func (c *Config) CheckAndMarkReloadInProgress() bool {
	c.Main.stalenessCheckLock.Lock()
	defer c.Main.stalenessCheckLock.Unlock()
	if c.Main == nil || 
	    (c.Main.configFilePath == "" && len(c.Main.configFilesPath) == 0) ||
		time.Now().Before(c.Main.configRateLimitTime) {
		return false
	}
	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}
	c.Main.configRateLimitTime = time.Now().Add(c.MgmtConfig.ReloadRateLimit)

	if len(c.Main.configFilesPath) > 0 {
		for index, file := range c.Main.configFilesPath {
			t := c.CheckFileLastModified(file)
			if t.IsZero() {
				continue
			}
			if !t.Equal(c.Main.configFilesLastMod[index]) {
				c.Main.configFilesLastMod[index] = t
				return true
			} 
		}
		return false
	}

	t := c.CheckFileLastModified("")
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

	// strip Redis password
	for k, v := range cp.Caches {
		if v != nil && cp.Caches[k].Redis != nil && cp.Caches[k].Redis.Password != "" {
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
		if len(c.Main.configFilesPath) == 0 {
			return c.Main.configFilePath	
		}
		return c.Flags.ConfigPath
	}
	return ""
}
