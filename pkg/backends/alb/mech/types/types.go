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

// RegistryEntry defines an entry in the ALB Registry
type RegistryEntry struct {
	Name      Name
	ShortName Name
	New       NewMechanismFunc
}
