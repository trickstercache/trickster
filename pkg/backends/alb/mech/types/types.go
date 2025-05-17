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
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
)

// ID defines the load balancing mechanism identifier type
type ID byte

// Name is a type alias for the load balancing mechanism common name
type Name string

// NewMechanismFunc defines a function that returns a new Mechanism from the
// provided Options
type NewMechanismFunc func(*options.Options, rt.Lookup) (Mechanism, error)

// Mechanism represents a specific ALB Implmentation (e.g., a Round Robiner)
type Mechanism interface {
	http.Handler
	SetPool(pool.Pool)
	StopPool()
	ID() ID
	Name() Name
}

// RegistryEntry defines an entry in the ALB Registry
type RegistryEntry struct {
	ID        ID
	Name      Name
	ShortName Name
	New       NewMechanismFunc
}
