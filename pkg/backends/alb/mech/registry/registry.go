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
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/nlm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
)

type entry struct {
	ID        mech.ID
	Name      mech.Name
	ShortName mech.Name
	New       mech.NewMechanismFunc
}

// this slice is the one and only place to add registered Mechanisms
var registry = []entry{
	{rr.ID, rr.Name, rr.ShortName, rr.New},
	{nlm.ID, nlm.Name, nlm.ShortName, nlm.New},
	{fr.ID, fr.Name, fr.ShortName, fr.New},
	{fr.FGRID, fr.FGRName, fr.FGRShortName, fr.NewFGR},
	{tsm.ID, tsm.Name, tsm.ShortName, tsm.New},
}

var registryByName = compileSupportedByName()

func compileSupportedByName() map[mech.Name]mech.NewMechanismFunc {
	out := make(map[mech.Name]mech.NewMechanismFunc, len(registry)*2)
	for _, entry := range registry {
		out[entry.ShortName] = entry.New
		out[entry.Name] = entry.New
	}
	return out
}

func New(name mech.Name, opts *options.Options,
	factories types.Lookup) (mech.Mechanism, error) {
	if f, ok := registryByName[name]; ok && f != nil {
		return f(opts, factories)
	}
	return nil, errors.ErrUnsupportedMechanism
}

func IsRegistered(name mech.Name) bool {
	_, ok := registryByName[name]
	return ok
}
