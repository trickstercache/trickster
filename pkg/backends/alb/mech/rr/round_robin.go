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

package rr

import (
	"net/http"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
)

const ID mech.ID = 0
const ShortName mech.Name = "rr"
const Name mech.Name = "round_robin"

type client struct {
	pool pool.Pool
	pos  atomic.Uint64
}

func New(_ *options.Options, _ types.Lookup) (mech.Mechanism, error) {
	return &client{}, nil
}

func (c *client) SetPool(p pool.Pool) {
	c.pool = p
}

func (c *client) ID() mech.ID {
	return ID
}

func (c *client) Name() mech.Name {
	return ShortName
}

func (c *client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if c.pool == nil {
		handlers.HandleBadGateway(w, r)
		return
	}
	t := c.nextTarget()
	if t != nil {
		t.ServeHTTP(w, r)
		return
	}
	handlers.HandleBadGateway(w, r)
}

func (c *client) StopPool() {
	if c.pool != nil {
		c.pool.Stop()
	}
}

func (c *client) nextTarget() http.Handler {
	healthy := c.pool.Healthy()
	if len(healthy) == 0 {
		return nil
	}
	i := c.pos.Add(1) % uint64(len(healthy))
	return healthy[i]
}
