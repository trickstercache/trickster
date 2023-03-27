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
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.Backend

	pool            pool.Pool
	handler         http.Handler // this is the actual handler for all request to this backend
	fgr             bool
	fgrCodes        map[int]interface{}
	mergePaths      []string     // paths handled by the alb client that are enabled for tsmerge
	nonmergeHandler http.Handler // when methodology is tsmerge, this handler is for non-mergeable paths
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
		switch o.ALBOptions.MechanismName {
		case pool.FirstResponse.String():
			c.handler = http.HandlerFunc(c.handleFirstResponse)
		case pool.FirstGoodResponse.String():
			c.handler = http.HandlerFunc(c.handleFirstResponse)
			c.fgr = true
			c.fgrCodes = o.ALBOptions.FgrCodesLookup
		case pool.NewestLastModified.String():
			c.handler = http.HandlerFunc(c.handleNewestResponse)
		case pool.TimeSeriesMerge.String():
			c.handler = http.HandlerFunc(c.handleResponseMerge)
			c.nonmergeHandler = http.HandlerFunc(c.handleRoundRobin)
			// this validates the merge configuration for the ALB client as it sets it up
			// First, verify the output format is a support merge provider
			if !providers.IsSupportedTimeSeriesMergeProvider(o.ALBOptions.OutputFormat) {
				return nil, ErrInvalidTimeSeriesMergeProvider
			}
			// next, get the factory function required to create a backend client for the supplied format
			f, ok := factories[o.ALBOptions.OutputFormat]
			if !ok {
				return nil, ErrInvalidTimeSeriesMergeProvider
			}
			// now, create a client for the merge provider based on the supplied factory function
			mc1, err := f("alb", nil, nil, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			// convert the new time series client to a mergeable timeseries client to get the merge paths
			mc2, ok := mc1.(backends.MergeableTimeseriesBackend)
			if !ok {
				return nil, ErrInvalidTimeSeriesMergeProvider
			}
			// set the merge paths in the ALB client
			c.mergePaths = mc2.MergeablePaths()
		default:
			c.handler = http.HandlerFunc(c.handleRoundRobin)
		}
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

// ValidatePools iterates the backends and validates ALB backends
func ValidatePools(clients backends.Backends) error {
	for _, v := range clients {
		if v.Configuration().Provider != "alb" {
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
	_, ok := pool.GetMechanismByName(c.Configuration().ALBOptions.MechanismName)
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
		return errors.New("invalid options")
	}

	o := c.Configuration().ALBOptions

	m, ok := pool.GetMechanismByName(o.MechanismName)
	if !ok {
		return fmt.Errorf("invalid mechanism name [%s] in backend [%s]", o.MechanismName, c.Name())
	}
	targets := make([]*pool.Target, 0, len(o.Pool))
	for _, n := range o.Pool {
		tc, ok := clients[n]
		if !ok {
			return fmt.Errorf("invalid pool member name [%s] in backend [%s]", n, c.Name())
		}
		hc, _ := hcs[n]
		targets = append(targets, pool.NewTarget(tc.Router(), hc))
	}
	c.pool = pool.New(m, targets, o.HealthyFloor)
	return nil
}

// Boilerplate Interface Functions (to EOF)

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {
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
