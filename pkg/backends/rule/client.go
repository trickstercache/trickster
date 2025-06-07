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

package rule

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.Backend

	// this exists so the rule can route the request to a destination by router name
	clients backends.Backends

	rule       *rule
	pathPrefix string
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Rules Router client reference
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, clients backends.Backends,
	_ types.Lookup) (backends.Backend, error) {

	c := &Client{
		clients:    clients,
		pathPrefix: "/" + name,
	}
	b, err := backends.New(name, o, c.RegisterHandlers, router, nil)
	c.Backend = b
	return c, err
}

// Clients is a list of *rule.Client
type Clients []*Client

// ValidateOptions ensures that rule clients are fully loaded, which can't be done
// until all backends are processed, so the rule's destination origin names
// can be mapped to their respective clients
func ValidateOptions(clients backends.Backends,
	rwi rewriter.InstructionsLookup) error {
	ruleClients := make(Clients, 0, len(clients))
	for _, c := range clients {
		if rc, ok := c.(*Client); ok {
			ruleClients = append(ruleClients, rc)
		}
	}
	if len(ruleClients) > 0 {
		if err := ruleClients.validate(rwi); err != nil {
			return err
		}
	}
	return nil
}

// Validate will fully load the Clients from their options and return an error if the options
// could not be validated
func (rc Clients) validate(rwi rewriter.InstructionsLookup) error {
	for _, c := range rc {
		if c == nil {
			return errors.ErrInvalidRuleOptions
		}
		cfg := c.Configuration()
		if cfg != nil {
			if err := c.parseOptions(cfg.RuleOptions, rwi); err != nil {
				return err
			}
		}
	}
	return nil
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.Lookup {
	return po.List{
		{
			Path:          "/",
			HandlerName:   providers.Rule,
			Methods:       methods.AllHTTPMethods(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}.ToLookup()
}

func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.Backend.RegisterHandlers(
		handlers.Lookup{
			"rule": http.HandlerFunc(c.Handler),
		},
	)
}
