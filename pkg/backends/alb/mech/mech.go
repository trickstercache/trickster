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

package mech

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
)

// id defines the load balancing mechanism identifier type
type ID byte

// Name is a type alias for the load balancing mechanism common name
type Name string

type NewMechanismFunc func(*options.Options, types.Lookup) (Mechanism, error)

type Mechanism interface {
	http.Handler
	SetPool(pool.Pool)
	StopPool()
	ID() ID
	Name() Name
}
