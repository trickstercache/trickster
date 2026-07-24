/*
 * Copyright 2026 The Trickster Authors
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

package config

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	frontend "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	"gopkg.in/yaml.v2"
)

const (
	legacyFrontendWarning = "frontend listener options are deprecated; configure listeners.default instead"
	legacyMetricsWarning  = "metrics listener options are deprecated; configure listeners.metrics instead"
	legacyMgmtWarning     = "mgmt listener port options are deprecated; configure listeners.mgmt instead"
)

func (c *Config) detectListenerSections(yml string) error {
	raw := make(map[interface{}]interface{})
	if err := yaml.Unmarshal([]byte(yml), &raw); err != nil {
		return err
	}
	c.listenerOverrides = make(map[string][]byte)
	if values, ok := raw["listeners"].(map[interface{}]interface{}); ok {
		for rawName, value := range values {
			if name, ok := rawName.(string); ok {
				data, err := yaml.Marshal(value)
				if err != nil {
					return fmt.Errorf("marshal listener %q override: %w", name, err)
				}
				c.listenerOverrides[name] = data
			}
		}
	}
	_, c.legacyFrontendUsed = raw["frontend"]
	_, c.legacyMetricsUsed = raw["metrics"]
	if values, ok := raw["mgmt"].(map[interface{}]interface{}); ok {
		_, hasAddress := values["listen_address"]
		_, hasPort := values["listen_port"]
		c.legacyMgmtUsed = hasAddress || hasPort
	}
	return nil
}

func (c *Config) applyLegacyListenerOptions() error {
	if c.Listeners == nil {
		c.Listeners = listener.NewLookup()
	}
	if c.Frontend != nil {
		legacyFrontend := c.Frontend.Clone()
		legacyFrontend.ListenPort = normalizedLegacyPort(legacyFrontend.ListenPort)
		legacyFrontend.TLSListenPort = normalizedLegacyPort(legacyFrontend.TLSListenPort)
		c.Listeners[listener.DefaultFrontendName] = listener.FromFrontend(legacyFrontend)
		c.Listeners[listener.DefaultFrontendName].Protocol = listener.ProtocolHTTP
		c.Listeners[listener.DefaultFrontendName].Active = true
	}

	o := listener.New(mgmt.ListenerNameMgmt)
	inheritLegacyFrontendSettings(o, c.Frontend)
	if c.MgmtConfig != nil {
		o.ListenAddress = c.MgmtConfig.ListenAddress
		o.ListenPort = normalizedLegacyPort(c.MgmtConfig.ListenPort)
	}
	c.Listeners[mgmt.ListenerNameMgmt] = o

	o = listener.New(mgmt.ListenerNameMetrics)
	inheritLegacyFrontendSettings(o, c.Frontend)
	if c.Metrics != nil {
		o.ListenAddress = c.Metrics.ListenAddress
		o.ListenPort = normalizedLegacyPort(c.Metrics.ListenPort)
	}
	c.Listeners[mgmt.ListenerNameMetrics] = o

	// Canonical listener values are applied last and therefore win field by
	// field over compatibility settings.
	for name, data := range c.listenerOverrides {
		o := c.Listeners[name]
		if o == nil {
			o = listener.New(name)
		}
		if err := yaml.Unmarshal(data, o); err != nil {
			return fmt.Errorf("apply listener %q override: %w", name, err)
		}
		if o.Protocol == "" {
			o.Protocol = listener.ProtocolHTTP
		}
		c.Listeners[name] = o
	}

	if c.legacyFrontendUsed {
		c.addLoaderWarning(legacyFrontendWarning)
	}
	if c.legacyMetricsUsed {
		c.addLoaderWarning(legacyMetricsWarning)
	}
	if c.legacyMgmtUsed {
		c.addLoaderWarning(legacyMgmtWarning)
	}
	return nil
}

func normalizedLegacyPort(port int) int {
	if port < 0 {
		return 0
	}
	return port
}

func inheritLegacyFrontendSettings(dst *listener.Options, src *frontend.Options) {
	if dst == nil || src == nil {
		return
	}
	dst.ConnectionsLimit = src.ConnectionsLimit
	dst.MaxRequestBodySizeBytes = src.Clone().MaxRequestBodySizeBytes
	dst.TruncateRequestBodyTooLarge = src.TruncateRequestBodyTooLarge
	dst.ReadHeaderTimeout = src.ReadHeaderTimeout
}

func (c *Config) addLoaderWarning(warning string) {
	for _, existing := range c.LoaderWarnings {
		if existing == warning {
			return
		}
	}
	c.LoaderWarnings = append(c.LoaderWarnings, warning)
}

// FrontendOptionsForListener returns the frontend-compatible options for name.
func (c *Config) FrontendOptionsForListener(name string) *frontend.Options {
	if c == nil || c.Listeners == nil {
		return nil
	}
	o, ok := c.Listeners[name]
	if !ok || o == nil {
		return nil
	}
	return o.FrontendOptions()
}

// RequireListener returns a configured listener or an error naming the missing listener.
func (c *Config) RequireListener(name string) (*listener.Options, error) {
	if c != nil {
		if o, ok := c.Listeners[name]; ok && o != nil {
			return o, nil
		}
	}
	return nil, fmt.Errorf("listener %q is not configured", name)
}
