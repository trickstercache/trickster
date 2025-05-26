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
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	fropt "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

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
	// TracingConfigs provides the distributed tracing configuration
	TracingConfigs tracing.Lookup `yaml:"tracing,omitempty"`
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
	Resources *Resources `yaml:"-"`

	CompiledRewriters      rewriter.InstructionsLookup `yaml:"-"`
	CompiledNegativeCaches negative.Lookups            `yaml:"-"`
	activeCaches           sets.Set[string]
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
	configLastModified  time.Time
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

// Resources is a collection of values used by configs at runtime that are not part of the config itself
type Resources struct {
	metadata yamlx.KeyLookup
}

// NewConfig returns a Config initialized with default values.
func NewConfig() *Config {
	hn, _ := os.Hostname()
	return &Config{
		Caches: cache.Lookup{
			"default": cache.New(),
		},
		Logging: lo.New(),
		Main: &MainConfig{
			ServerName: hn,
		},
		MgmtConfig: mgmt.New(),
		Metrics:    mo.New(),
		Backends: bo.Lookup{
			"default": bo.New(),
		},
		Frontend: fropt.New(),
		NegativeCacheConfigs: negative.ConfigLookup{
			"default": negative.New(),
		},
		TracingConfigs: tracing.Lookup{
			"default": tracing.New(),
		},
		LoaderWarnings: make([]string, 0),
		Resources:      &Resources{},
	}
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
	c.Main.configLastModified = c.CheckFileLastModified()
	return nil
}

// loadYAMLConfig loads application configuration from a YAML-formatted byte slice.
func (c *Config) loadYAMLConfig(yml string) error {
	err := yaml.Unmarshal([]byte(yml), &c)
	if err != nil {
		return err
	}
	md, err := yamlx.GetKeyList(yml)
	if err != nil {
		return err
	}
	if c.Resources == nil {
		c.Resources = &Resources{}
	}
	return c.OverlayYAMLData(md)
}

// OverlayYAMLData extracts supported Config values from the yaml map,
// overlays the extracted values onto c.
func (c *Config) OverlayYAMLData(md yamlx.KeyLookup) error {
	c.Resources.metadata = md
	c.activeCaches = sets.NewStringSet()
	for k, v := range c.Backends {
		w, err := bo.OverlayYAMLData(k, v, c.Backends, c.activeCaches, md)
		if err != nil {
			return err
		}
		c.Backends[k] = w
	}
	if lw, err := c.Caches.OverlayYAMLData(c.Resources.metadata,
		c.activeCaches); err != nil {
		return err
	} else if len(lw) > 0 {
		c.LoaderWarnings = append(c.LoaderWarnings, lw...)
	}
	return nil
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
			for k, p := range b.Paths {
				if p.ReqRewriterName != "" {
					ri, ok := c.CompiledRewriters[p.ReqRewriterName]
					if !ok {
						return fmt.Errorf("invalid rewriter name %s in path %s of backend options %s",
							p.ReqRewriterName, k, b.Name)
					}
					p.ReqRewriter = ri
				}
			}
		}
	}
	tracing.ProcessTracingOptions(c.TracingConfigs, c.Resources.metadata)
	return nil
}

// Clone returns an exact copy of the subject *Config
func (c *Config) Clone() *Config {

	nc := NewConfig()
	delete(nc.Caches, "default")
	delete(nc.Backends, "default")

	nc.Main.InstanceID = c.Main.InstanceID
	nc.Main.ServerName = c.Main.ServerName

	nc.MgmtConfig = c.MgmtConfig.Clone()

	nc.Main.configFilePath = c.Main.configFilePath
	nc.Main.configLastModified = c.Main.configLastModified
	nc.Main.configRateLimitTime = c.Main.configRateLimitTime

	nc.Metrics.ListenAddress = c.Metrics.ListenAddress
	nc.Metrics.ListenPort = c.Metrics.ListenPort

	if c.Frontend != nil {
		nc.Frontend = c.Frontend.Clone()
	}

	nc.Resources = &Resources{}

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

	if c.Main == nil || c.Main.configFilePath == "" ||
		time.Now().Before(c.Main.configRateLimitTime) {
		return false
	}

	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}

	c.Main.configRateLimitTime =
		time.Now().Add(c.MgmtConfig.ReloadRateLimit)
	t := c.CheckFileLastModified()
	if t.IsZero() {
		return false
	}
	return !t.Equal(c.Main.configLastModified)
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
