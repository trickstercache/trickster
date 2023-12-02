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

// Package backends the interface and generic functionality for Backend providers
package backends

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

// Backends represents a map of Backends keyed by Name
type Backends map[string]Backend

// StartHealthChecks iterates the backends to fully configure health checkers
// and start up any intervaled health checks
func (b Backends) StartHealthChecks(logger interface{}) (healthcheck.HealthChecker, error) {
	hc := healthcheck.New()
	for k, c := range b {
		bo := c.Configuration()
		if IsVirtual(bo.Provider) || k == "frontend" {
			continue
		}
		hco := bo.HealthCheck
		if hco == nil {
			continue
		}
		bo.HealthCheck = c.DefaultHealthCheckConfig()
		if bo.HealthCheck == nil {
			bo.HealthCheck = hco
		} else {
			bo.HealthCheck.Overlay(k, hco)
		}
		st, err := hc.Register(k, bo.Provider, bo.HealthCheck, c.HealthCheckHTTPClient(), logger)
		if err != nil {
			return nil, err
		}
		c.SetHealthCheckProbe(st.Prober())
	}
	return hc, nil
}

// Get returns the named origin
func (b Backends) Get(backendName string) Backend {
	if c, ok := b[backendName]; ok {
		return c
	}
	return nil
}

// GetConfig returns the named Backend's Configuration Options
func (b Backends) GetConfig(backendName string) *bo.Options {
	if c, ok := b[backendName]; ok {
		return c.Configuration()
	}
	return nil
}

// GetRouter returns the named Backend's Request Router
func (b Backends) GetRouter(backendName string) http.Handler {
	if c, ok := b[backendName]; ok {
		return c.Router()
	}
	return nil
}

// IsVirtual returns true if the backend is a virtual type (e.g., ones that do not
// make an outbound http request, but instead front to other backends)
func IsVirtual(provider string) bool {
	return provider == "alb" || provider == "rule"
}

// UsesCache returns true if the backend uses a cache
// (anything except Virtuals and ReverseProxy)
func UsesCache(provider string) bool {
	return !(IsVirtual(provider)) && !(provider == "rp") && !(provider == "reverseproxy")
}
