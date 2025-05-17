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

package registry

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/nlm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
)

// this slice is the one and only place to define all registered Mechanisms
var registry = []types.RegistryEntry{
	{ID: rr.ID, Name: rr.Name, ShortName: rr.ShortName, New: rr.New},
	{ID: nlm.ID, Name: nlm.Name, ShortName: nlm.ShortName, New: nlm.New},
	{ID: fr.ID, Name: fr.Name, ShortName: fr.ShortName, New: fr.New},
	{ID: fr.FGRID, Name: fr.FGRName, ShortName: fr.FGRShortName, New: fr.NewFGR},
	{ID: tsm.ID, Name: tsm.Name, ShortName: tsm.ShortName, New: tsm.New},
}

var registryByName = compileSupportedByName()

func compileSupportedByName() map[types.Name]types.NewMechanismFunc {
	out := make(map[types.Name]types.NewMechanismFunc, len(registry)*2)
	for _, entry := range registry {
		out[entry.ShortName] = entry.New
		out[entry.Name] = entry.New
	}
	return out
}

func New(name types.Name, opts *options.Options,
	factories rt.Lookup) (types.Mechanism, error) {
	if f, ok := registryByName[name]; ok && f != nil {
		return f(opts, factories)
	}
	return nil, errors.ErrUnsupportedMechanism
}

func IsRegistered(name types.Name) bool {
	_, ok := registryByName[name]
	return ok
}
