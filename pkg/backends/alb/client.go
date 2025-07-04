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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/registry"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	authopt "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	authreg "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Client Implements the Backend Interface
type Client struct {
	backends.Backend
	handler types.Mechanism // this is the actual handler for all request to this backend
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() handlers.Lookup {
	return handlers.Lookup{providers.ALB: c.handler}
}

var _ rt.NewBackendClientFunc = NewClient

// NewClient returns a new ALB client reference
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, _ backends.Backends, factories rt.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, nil, router, nil)
	if err != nil {
		return nil, err
	}
	c.Backend = b
	if o != nil && o.ALBOptions != nil {
		m, err := registry.New(types.Name(o.ALBOptions.MechanismName),
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

// ValidateClients iterates the backends and validates ALB backends
func ValidateClients(clients backends.Backends) error {
	backends := sets.MapKeysToStringSet(clients)
	for _, v := range clients {
		if v == nil || v.Configuration().Provider != providers.ALB {
			continue
		}
		if c, ok := v.(*Client); ok {
			err := c.Validate(backends)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidatePool confirms the provided list of backends is valid
func (c *Client) Validate(backends sets.Set[string]) error {
	o := c.Configuration()
	if o.ALBOptions == nil {
		return errors.ErrInvalidOptions
	}
	if !registry.IsRegistered(types.Name(o.ALBOptions.MechanismName)) {
		return fmt.Errorf("invalid mechanism name [%s] in backend [%s]",
			o.ALBOptions.MechanismName, o.Name)
	}
	return c.ValidatePool(backends)
}

// ValidatePool confirms the provided list of backends is valid
func (c *Client) ValidatePool(backends sets.Set[string]) error {
	o := c.Configuration().ALBOptions
	if o == nil {
		return errors.ErrInvalidOptions
	}
	return o.ValidatePool(c.Name(), backends)
}

// ValidateAndStartPool starts this Client's pool up using the provided list of
// backends to validate and map out the pool configuration
func (c *Client) ValidateAndStartPool(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	if c.Configuration() == nil || c.Configuration().ALBOptions == nil {
		return errors.ErrInvalidOptions
	}
	o := c.Configuration().ALBOptions
	err := c.ValidatePool(sets.MapKeysToStringSet(clients))
	if err != nil {
		return err
	}
	if o.MechanismName == string(ur.ShortName) && o.UserRouter != nil {
		return c.validateAndStartUserRouter(clients)
	}
	targets := make([]*pool.Target, 0, len(o.Pool))
	for _, n := range o.Pool {
		tc, ok := clients[n]
		if !ok {
			return alberr.NewErrInvalidPoolMemberName(c.Name(), n)
		}
		hc, ok := hcs[n]
		if !ok {
			continue // virtual backends (rule, alb) don't currently have health checks
		}
		targets = append(targets, pool.NewTarget(tc.Router(), hc))
	}
	if c.handler != nil {
		c.handler.SetPool(pool.New(targets, o.HealthyFloor))
	}
	return nil
}

func observeOnlyOpts() *authopt.Options {
	return &authopt.Options{ObserveOnly: true}
}

func (c *Client) validateAndStartUserRouter(clients backends.Backends) error {
	conf := c.Configuration()
	var canReplaceCreds bool
	o := conf.ALBOptions.UserRouter
	h, ok := c.handler.(*ur.Handler)
	if !ok {
		return nil
	}
	if conf.AuthOptions != nil && conf.AuthOptions.Authenticator != nil {
		// credential replacement is only allowed if users will be positively
		// authenticated and not just observed.
		canReplaceCreds = !(conf.AuthOptions.Authenticator.IsObserveOnly())
		h.SetAuthenticator(conf.AuthOptions.Authenticator, canReplaceCreds)
	} else {
		a, err := authreg.NewObserverFromProviderName(o.TargetProvider,
			map[string]any{"options": observeOnlyOpts()})
		if err != nil {
			return err
		} else if a == nil {
			return errors.ErrInvalidOptions
		}
		h.SetAuthenticator(a, false)
	}
	if o.DefaultBackend != "" {
		bh, ok := clients[o.DefaultBackend]
		if !ok || bh == nil {
			return alberr.NewErrInvalidBackendName(c.Name(), o.DefaultBackend)
		}
		h.SetDefaultHandler(bh.Router())
	} else {
		if o.NoRouteStatusCode < http.StatusBadRequest || o.NoRouteStatusCode >= 600 {
			o.NoRouteStatusCode = http.StatusBadGateway
		}
		switch o.NoRouteStatusCode {
		case http.StatusUnauthorized:
			h.SetDefaultHandler(http.HandlerFunc(failures.HandleUnauthorized))
		case http.StatusBadGateway:
			h.SetDefaultHandler(http.HandlerFunc(failures.HandleBadGateway))
		case http.StatusBadRequest:
			h.SetDefaultHandler(http.HandlerFunc(failures.HandleBadRequestResponse))
		case http.StatusInternalServerError:
			h.SetDefaultHandler(http.HandlerFunc(failures.HandleInternalServerError))
		case http.StatusNotFound:
			h.SetDefaultHandler(http.HandlerFunc(failures.HandleNotFound))
		default:
			h.SetDefaultHandler(http.HandlerFunc(func(w http.ResponseWriter,
				_ *http.Request) {
				failures.HandleMiscFailure(o.NoRouteStatusCode, w)
			}))
		}
	}

	for _, m := range o.Users {
		if m.ToBackend != "" {
			bh, ok := clients[m.ToBackend]
			if !ok || bh == nil {
				return alberr.NewErrInvalidBackendName(c.Name(), m.ToBackend)
			}
			m.ToHandler = bh.Router()
		}
		if !canReplaceCreds && m.ToCredential != "" {
			return alberr.NewErrInvalidUserRouterCreds(c.Name())
		}
	}

	return nil
}

// StopPool stops this Client's pool
func (c *Client) StopPool() {
	c.handler.StopPool()
}

// Boilerplate Interface Functions (to EOF)

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.Lookup {
	return po.List{
		{
			Path:          "/",
			HandlerName:   providers.ALB,
			Methods:       methods.AllHTTPMethods(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}.ToLookup()
}
