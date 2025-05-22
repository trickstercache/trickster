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

package validate

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registry"
	ar "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

func Validate(c *config.Config) error {
	if c.ReloadConfig != nil {
		if err := c.ReloadConfig.Validate(); err != nil {
			return err
		}
	}
	if c.Logging != nil {
		if err := c.Logging.Validate(); err != nil {
			return err
		}
	}
	if c.Metrics != nil {
		if err := c.Metrics.Validate(); err != nil {
			return err
		}
	}
	if err := Tracers(c); err != nil {
		return err
	}
	if err := Rewriters(c); err != nil {
		return err
	}
	if err := Rules(c); err != nil {
		return err
	}
	if err := Authenticators(c); err != nil {
		return err
	}
	if err := Caches(c); err != nil {
		return err
	}
	if err := NegativeCaches(c); err != nil {
		return err
	}
	if err := Backends(c); err != nil {
		return err
	}
	if c.Frontend != nil {
		if err := c.Frontend.Validate(c.TLSCertConfig); err != nil {
			return err
		}
	}
	return nil
}

func Rewriters(c *config.Config) error {
	if len(c.RequestRewriters) == 0 {
		return nil
	}
	return c.RequestRewriters.Validate()
}

func Tracers(c *config.Config) error {
	if len(c.TracingConfigs) == 0 {
		return nil
	}
	return c.TracingConfigs.Validate()
}

func Rules(c *config.Config) error {
	if len(c.Rules) == 0 {
		return nil
	}
	return c.Rules.Validate()
}

func Caches(c *config.Config) error {
	if len(c.Caches) == 0 {
		return nil
	}
	return c.Caches.Validate()
}

func NegativeCaches(c *config.Config) error {
	if len(c.NegativeCacheConfigs) == 0 {
		return nil
	}
	nc, err := c.NegativeCacheConfigs.ValidateAndCompile()
	if err != nil {
		return err
	}
	c.CompiledNegativeCaches = nc
	return nil
}

func Authenticators(c *config.Config) error {
	if len(c.Authenticators) == 0 {
		return nil
	}
	if err := c.Authenticators.Validate(ar.IsRegistered); err != nil {
		return err
	}
	return nil
}

func Backends(c *config.Config) error {
	if len(c.Backends) == 0 {
		return errors.ErrNoValidBackends
	}
	if err := c.Backends.ValidateConfigMappings(c.Caches, c.CompiledNegativeCaches,
		c.Rules, c.RequestRewriters, c.Authenticators, c.TracingConfigs); err != nil {
		return err
	}
	serveTLS, err := c.Backends.ValidateTLSConfigs()
	if err != nil {
		return err
	}
	if serveTLS {
		c.Frontend.ServeTLS = true
	}
	return c.Backends.Validate()
}

func RoutesRulesAndPools(c *config.Config) error {
	var caches = make(cache.Lookup)
	for k := range c.Caches {
		caches[k] = nil
	}
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only
	tracers, err := tr.RegisterAll(c, true)
	if err != nil {
		return err
	}
	clients, err := routing.RegisterProxyRoutes(c, r, mr, caches, tracers, true)
	if err != nil {
		return err
	}
	// these validations can't be performed until the router tree is constructed
	err = rule.ValidateOptions(clients, c.CompiledRewriters)
	if err != nil {
		return err
	}
	return alb.ValidateClients(clients)
}
