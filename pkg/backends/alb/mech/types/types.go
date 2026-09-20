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

package types

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is a type alias for the load balancing mechanism common name
type Name = string

// NewMechanismFunc defines a function that returns a new Mechanism from the
// provided Options
type NewMechanismFunc func(*options.Options, types.Lookup) (Mechanism, error)

// Mechanism represents a specific ALB Implementation (e.g., a Round Robiner).
// Pool-aware mechanisms additionally implement PoolMechanism; callers that
// need to drive a pool must type-assert before invoking pool methods.
type Mechanism interface {
	http.Handler
	Name() Name
}

// PoolMechanism is the subset of Mechanism that owns a backend pool. Mechs
// such as round_robin, first_response, newest_last_modified, and
// time_series_merge implement it; user_router does not.
type PoolMechanism interface {
	Mechanism
	SetPool(pool.Pool)
	StopPool()
	Pool() pool.Pool
}

// NewSelectorFunc returns a new selection strategy from the provided Options. It is where
// config is translated into the protocol-neutral core's own option types.
type NewSelectorFunc func(*options.Options) (lb.Selector, error)

// Plane is a set of dispatch planes: the kinds of listener a mechanism can serve.
type Plane uint8

const (
	// PlaneHTTP is request dispatch through an http.Handler.
	PlaneHTTP Plane = 1 << iota
	// PlaneStream is tcp, tls and udp relaying.
	PlaneStream
	// PlaneNative is a wire-protocol session server, such as MySQL.
	PlaneNative
)

// Has reports whether every plane in want is in the set.
func (p Plane) Has(want Plane) bool {
	return want != 0 && p&want == want
}

// PickerMechanism is a pool mechanism that selects one member per unit of work, and so can
// serve planes other than HTTP through its Picker.
type PickerMechanism interface {
	PoolMechanism
	Picker() lb.Picker
	Balancer() *lb.Balancer
}

// RegistryEntry defines an entry in the ALB Registry. Exactly one of New and NewSelector is
// set: New for a mechanism that is itself an HTTP handler, NewSelector for a strategy that
// the registry wraps for whichever plane asks.
type RegistryEntry struct {
	Name        Name
	ShortName   Name
	Planes      Plane
	New         NewMechanismFunc
	NewSelector NewSelectorFunc
}
