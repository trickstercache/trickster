/*
 * Copyright 2026 The Trickster Authors
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

// Package spread holds the mechanisms that commit one flow to several pool members at once:
// a connect race for tcp and tls, and datagram mirroring for udp. Only a stream listener can
// serve them; they hold the pool, and the stream relay does the rest.
package spread

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
)

// the stream protocols each mechanism serves, as a listener spells them
const (
	protocolTCP = "tcp"
	protocolTLS = "tls"
	protocolUDP = "udp"
)

type handler struct {
	mech.PoolHolder
	name   types.Name
	spread types.Spread
}

// RegistryEntryRace returns the registry entry of the connect race mechanism.
func RegistryEntryRace() types.RegistryEntry {
	return types.RegistryEntry{
		Name: names.MechanismConnectRace, ShortName: names.MechanismRace, Planes: types.PlaneStream,
		Protocols: []string{protocolTCP, protocolTLS},
		New: func(*options.Options, rt.Lookup) (types.Mechanism, error) {
			return &handler{name: names.MechanismRace, spread: types.SpreadRace}, nil
		},
	}
}

// RegistryEntryMirror returns the registry entry of the datagram mirror mechanism.
func RegistryEntryMirror() types.RegistryEntry {
	return types.RegistryEntry{
		Name: names.MechanismUDPMirror, ShortName: names.MechanismMirror, Planes: types.PlaneStream,
		Protocols: []string{protocolUDP},
		New: func(*options.Options, rt.Lookup) (types.Mechanism, error) {
			return &handler{name: names.MechanismMirror, spread: types.SpreadMirror}, nil
		},
	}
}

func (h *handler) Name() types.Name {
	return h.name
}

func (h *handler) Spread() types.Spread {
	return h.spread
}

func (h *handler) StopPool() {
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

// ServeHTTP refuses: config validation keeps these mechanisms off every request listener
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	failures.HandleBadGateway(w, r)
}
