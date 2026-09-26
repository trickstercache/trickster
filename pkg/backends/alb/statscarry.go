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
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// carriedStats holds each ALB's static members' runtime stats by name, so that a config
// reload, which rebuilds every ALB, does not send a latency-ranking mechanism back to knowing
// nothing about its members. Discovered members keep theirs through their own manager.
var carriedStats = struct {
	mtx   sync.Mutex
	byALB map[string]map[string]*lb.Stats
}{byALB: make(map[string]map[string]*lb.Stats)}

// carryStats returns the stats a member of the named ALB had before, if any
func carryStats(albName, member string) *lb.Stats {
	carriedStats.mtx.Lock()
	defer carriedStats.mtx.Unlock()
	return carriedStats.byALB[albName][member]
}

// rememberStats replaces what is carried for the named ALB with its current members' stats
func rememberStats(albName string, members map[string]*lb.Stats) {
	carriedStats.mtx.Lock()
	defer carriedStats.mtx.Unlock()
	if len(members) == 0 {
		delete(carriedStats.byALB, albName)
		return
	}
	carriedStats.byALB[albName] = members
}

// forgetStatsExcept drops what is carried for ALBs that the running config no longer has
func forgetStatsExcept(clients backends.Backends) {
	carriedStats.mtx.Lock()
	defer carriedStats.mtx.Unlock()
	for name := range carriedStats.byALB {
		if _, ok := clients[name].(*Client); !ok {
			delete(carriedStats.byALB, name)
		}
	}
}

// ForgetUnusedStickyTables drops every sticky table that no running ALB holds, such as one whose
// ALB left the config or stopped keeping its sessions in a table. A config build calls it once its
// listeners are running, as an ALB takes the table for its stream and native flows only when a
// listener first asks for it.
func ForgetUnusedStickyTables(clients backends.Backends) {
	sticky.ForgetTablesExcept(func(albName string, t *sticky.Table) bool {
		c, ok := clients[albName].(*Client)
		if !ok {
			return false
		}
		if c.flows.Load().Table() == t {
			return true
		}
		pm, ok := c.handler.(types.PickerMechanism)
		return ok && pm.StickyTable() == t
	})
}
