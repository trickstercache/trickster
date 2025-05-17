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

package alb

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/registry"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.Backend
	handler mech.Mechanism // this is the actual handler for all request to this backend
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	return map[string]http.Handler{"alb": c.handler}
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new ALB client reference
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, _ backends.Backends, factories types.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, nil, router, nil)
	if err != nil {
		return nil, err
	}
	c.Backend = b
	if o != nil && o.ALBOptions != nil {
		m, err := registry.New(mech.Name(o.ALBOptions.MechanismName),
			o.ALBOptions, factories)
		if err != nil {
			return nil, err
		}
		c.handler = m
	}
	return c, nil
}

// StartALBPools ensures that ALB's are fully loaded, which can't be done
// until all backends are processed, so the ALB's destination backend names
// can be mapped to their respective clients
func StartALBPools(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	for _, c := range clients {
		if rc, ok := c.(*Client); ok {
			err := rc.ValidateAndStartPool(clients, hcs)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// StopPools ensures that ALBs are fully stopped when the process's
// configuration is reloaded
func StopPools(clients backends.Backends) error {
	for _, c := range clients {
		if rc, ok := c.(*Client); ok {
			rc.StopPool()
		}
	}
	return nil
}

// ValidatePools iterates the backends and validates ALB backends
func ValidatePools(clients backends.Backends) error {
	for _, v := range clients {
		if v == nil || v.Configuration().Provider != "alb" {
			continue
		}
		if alb, ok := v.(*Client); ok {
			err := alb.ValidatePool(clients)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidatePool confirms the provided list of backends to is valid
func (c *Client) ValidatePool(clients backends.Backends) error {
	ok := registry.IsRegistered(mech.Name(c.Configuration().ALBOptions.MechanismName))
	if !ok {
		return fmt.Errorf("invalid mechanism name [%s] in backend [%s]",
			c.Configuration().ALBOptions.MechanismName, c.Name())
	}
	for _, n := range c.Configuration().ALBOptions.Pool {
		if _, ok := clients[n]; !ok {
			return fmt.Errorf("invalid pool member name [%s] in backend [%s]", n, c.Name())
		}
	}
	return nil
}

// ValidateAndStartPool starts this Client's pool up using the provided list of backends to
// validate and map out the pool configuration
func (c *Client) ValidateAndStartPool(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	if c.Configuration() == nil || c.Configuration().ALBOptions == nil {
		return errors.ErrInvalidOptions
	}
	o := c.Configuration().ALBOptions
	err := c.ValidatePool(clients)
	if err != nil {
		return err
	}
	targets := make([]*pool.Target, 0, len(o.Pool))
	for _, n := range o.Pool {
		tc, ok := clients[n]
		if !ok {
			return fmt.Errorf("invalid pool member name [%s] in backend [%s]", n, c.Name())
		}
		hc := hcs[n]
		targets = append(targets, pool.NewTarget(tc.Router(), hc))
	}
	if c.handler != nil {
		c.handler.SetPool(pool.New(targets, o.HealthyFloor))
	}
	return nil
}

// StopPool stops this Client's pool
func (c *Client) StopPool() {
	c.handler.StopPool()
}

// Boilerplate Interface Functions (to EOF)

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) map[string]*po.Options {
	m := methods.CacheableHTTPMethods()
	paths := map[string]*po.Options{
		"/" + strings.Join(m, "-"): {
			Path:          "/",
			HandlerName:   "alb",
			Methods:       m,
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}
	return paths
}
