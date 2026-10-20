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
	"context"
	"maps"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
)

type retiringMembers struct {
	mu      sync.Mutex
	members backends.Backends
	retired bool
}

// Retire retires what the mechanism runs of its own, as a global evaluator, once the ALB's requests have drained, then
// the discovered members its pool kept, which that work reads through.
func (c *Client) Retire(ctx context.Context) {
	backends.Retire(ctx, c.handler)
	c.retiring.mu.Lock()
	members := c.retiring.members
	c.retiring.members, c.retiring.retired = nil, true
	c.retiring.mu.Unlock()
	retireMembers(ctx, members)
}

// RetireWith has the ALB retire members, discovered clients its pool can still dispatch to once their manager stops, as it
// retires itself, or at once when it has already; until then they serve on, as they do for an ALB a failed reload restores.
func (c *Client) RetireWith(members backends.Backends) {
	if len(members) == 0 {
		return
	}
	c.retiring.mu.Lock()
	if !c.retiring.retired {
		if c.retiring.members == nil {
			c.retiring.members = make(backends.Backends, len(members))
		}
		maps.Copy(c.retiring.members, members)
		c.retiring.mu.Unlock()
		return
	}
	c.retiring.mu.Unlock()
	// the ALB's own work has ended, so nothing is left for the members to wait out
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retireMembers(ctx, members)
}

func retireMembers(ctx context.Context, members backends.Backends) {
	members.RetireContext(ctx)
	// connections released by requests that ended after the members' idle ones were last closed go too
	members.CloseIdleConnections()
}
