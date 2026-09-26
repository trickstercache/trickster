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
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/nlm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/pick"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/spread"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/observe"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/hrw"
	"github.com/trickstercache/trickster/v2/pkg/lb/lc"
	"github.com/trickstercache/trickster/v2/pkg/lb/lt"
	"github.com/trickstercache/trickster/v2/pkg/lb/p2c"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

// this slice is the one and only place to aggregate all registered Mechanisms
var registry = []types.RegistryEntry{
	roundRobin(),
	strategy(names.MechanismPowerOfTwoChoices, names.MechanismP2C, everyPlane,
		func(*options.Options) (lb.Selector, error) { return p2c.New(), nil }),
	strategy(names.MechanismHighestRandomWeight, names.MechanismHRW, everyPlane,
		func(*options.Options) (lb.Selector, error) { return hrw.New(), nil }),
	// a native session reports no latency for least time to rank members by
	strategy(names.MechanismLeastTime, names.MechanismLT, types.PlaneHTTP|types.PlaneStream,
		func(o *options.Options) (lb.Selector, error) {
			if o == nil {
				return lt.New(lt.Options{}), nil
			}
			return lt.New(lt.Options{Decay: o.LTDecay()}), nil
		}),
	strategy(names.MechanismLeastConnections, names.MechanismLC, everyPlane,
		func(*options.Options) (lb.Selector, error) { return lc.New(), nil }),
	fr.RegistryEntry(),
	fr.RegistryEntryFGR(),
	nlm.RegistryEntry(),
	tsm.RegistryEntry(),
	ur.RegistryEntry(),
	spread.RegistryEntryRace(),
	spread.RegistryEntryMirror(),
}

// roundRobin is a selection strategy, so it serves every plane that commits one unit of work
// to one member
func roundRobin() types.RegistryEntry {
	return types.RegistryEntry{
		Name:      names.MechanismRoundRobin,
		ShortName: names.MechanismRR,
		Planes:    everyPlane,
		NewSelector: func(*options.Options) (lb.Selector, error) {
			return rr.New(), nil
		},
	}
}

// everyPlane is where one unit of work is committed to one member: requests, tcp, tls and udp
// flows, and the sessions of a native protocol listener
const everyPlane = types.PlaneHTTP | types.PlaneStream | types.PlaneNative

// strategy registers a selection strategy for the planes it can serve
func strategy(name, shortName types.Name, planes types.Plane, fn types.NewSelectorFunc) types.RegistryEntry {
	return types.RegistryEntry{Name: name, ShortName: shortName, Planes: planes, NewSelector: fn}
}

var registryByName = compileSupportedByName(registry)

// compileSupportedByName indexes registry entries by both Name and ShortName.
// Panics on duplicate registration: silently last-write-wins would mask a
// configuration error that only surfaces at request time. It also panics on an
// entry that is not exactly one of a mechanism and a selection strategy.
func compileSupportedByName(entries []types.RegistryEntry) map[types.Name]types.RegistryEntry {
	out := make(map[types.Name]types.RegistryEntry, len(entries)*2)
	add := func(name types.Name, entry types.RegistryEntry) {
		if _, exists := out[name]; exists {
			panic("alb/mech/registry: duplicate mechanism name " + name)
		}
		out[name] = entry
	}

	for _, entry := range entries {
		if (entry.New == nil) == (entry.NewSelector == nil) {
			panic("alb/mech/registry: mechanism " + entry.Name + " must set exactly one of New and NewSelector")
		}
		add(entry.ShortName, entry)
		add(entry.Name, entry)
	}
	return out
}

// New returns the named mechanism as an HTTP handler. A selection strategy is wrapped in
// the handler that dispatches to the member it picks.
func New(name types.Name, opts *options.Options,
	factories rt.Lookup,
) (types.Mechanism, error) {
	entry, ok := registryByName[name]
	if !ok {
		return nil, errors.ErrUnsupportedMechanism
	}
	if entry.NewSelector == nil {
		return entry.New(opts, factories)
	}
	s, err := entry.NewSelector(opts)
	if err != nil {
		return nil, err
	}
	return pick.New(entry.ShortName, s, pickOptions(opts)), nil
}

// pickOptions carries to the HTTP adapter what a strategy's needs may call for
func pickOptions(o *options.Options) pick.Options {
	if o == nil {
		return pick.Options{}
	}
	return pick.Options{
		Key: o.HRW.KeySource, IPv6Prefix: o.HRW.IPv6Prefix, GoodCodes: o.LT.GoodCodes,
		Balancer: balancerOptions(o),
	}
}

// balancerOptions translates what configures the balancer itself: passive ejection, which
// only a stream listener's connect failures ever feed
func balancerOptions(o *options.Options) lb.BalancerOptions {
	bo := lb.BalancerOptions{Observer: observe.Balancer(o.Name)}
	if o.Stream != nil && o.Stream.PassiveHealth != nil {
		p := o.Stream.PassiveHealth
		bo.Ejection = lb.EjectionOptions{
			Failures: p.Failures, Duration: time.Duration(p.Eject), MaxPercent: p.MaxEjectedPercent,
		}
	}
	return bo
}

// NewBalancer returns the named selection strategy as a balancer with no pool, for a plane
// that does not dispatch over HTTP. A mechanism that is not a strategy is unsupported.
func NewBalancer(name types.Name, opts *options.Options) (*lb.Balancer, error) {
	entry, ok := registryByName[name]
	if !ok || entry.NewSelector == nil {
		return nil, errors.ErrUnsupportedMechanism
	}
	s, err := entry.NewSelector(opts)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		return lb.NewBalancer(s), nil
	}
	return lb.NewBalancer(s, balancerOptions(opts)), nil
}

func IsRegistered(name types.Name) bool {
	_, ok := registryByName[name]
	return ok
}

// Supports reports whether the named mechanism can serve every plane in plane.
func Supports(name types.Name, plane types.Plane) bool {
	entry, ok := registryByName[name]
	return ok && entry.Planes.Has(plane)
}

// ServesProtocol reports whether the named mechanism can serve a stream listener of the given
// protocol: tcp, tls or udp.
func ServesProtocol(name types.Name, protocol string) bool {
	entry, ok := registryByName[name]
	if !ok || !entry.Planes.Has(types.PlaneStream) {
		return false
	}
	return len(entry.Protocols) == 0 || slices.Contains(entry.Protocols, protocol)
}

// ServingProtocol returns the short names of the mechanisms that can serve a stream listener
// of the given protocol, sorted.
func ServingProtocol(protocol string) []types.Name {
	var out []types.Name
	for _, entry := range registry {
		if ServesProtocol(entry.ShortName, protocol) {
			out = append(out, entry.ShortName)
		}
	}
	slices.Sort(out)
	return out
}

// StreamProtocols returns the stream protocols the named mechanism is limited to, or nil
// when it serves them all or none.
func StreamProtocols(name types.Name) []string {
	return slices.Clone(registryByName[name].Protocols)
}

// Supporting returns the short names of the mechanisms that can serve plane, sorted.
func Supporting(plane types.Plane) []types.Name {
	var out []types.Name
	for _, entry := range registry {
		if entry.Planes.Has(plane) {
			out = append(out, entry.ShortName)
		}
	}
	slices.Sort(out)
	return out
}
