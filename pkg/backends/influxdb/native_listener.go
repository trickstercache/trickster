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

package influxdb

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flight"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	yamlencoding "github.com/trickstercache/trickster/v2/pkg/encoding/yaml"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

type nativeListenerAdapter struct{}

// NativeListenerAdapter returns InfluxDB's native-listener adapter, which
// exposes a backend over Apache Arrow Flight SQL (gRPC).
func NativeListenerAdapter() native.Adapter { return nativeListenerAdapter{} }

// SupportsHTTP is true because InfluxDB backends serve their primary HTTP
// interface through ordinary HTTP listeners; Flight SQL is an additional
// native endpoint.
func (nativeListenerAdapter) SupportsHTTP() bool { return true }

func (nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolInfluxDB }

func (nativeListenerAdapter) Configured(*listenerconfig.Options) bool { return false }

func (nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil InfluxDB listener options")
	}
	return nil
}

func (nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o == nil {
		return errors.New("nil InfluxDB backend options")
	}
	if _, err := flightUpstreamAddress(o); err != nil {
		return err
	}
	return nil
}

func (nativeListenerAdapter) ValidateUserRouter(*config.Config, string, *bo.Options) error {
	return errors.New("InfluxDB Flight SQL user routing is not supported")
}

func (nativeListenerAdapter) RouteResolver(native.BuildRequest) backends.RouteResolver { return nil }

// flightUpstreamAddress resolves the upstream Flight SQL host:port for a
// backend, preferring the explicit override and falling back to the origin
// URL's host.
func flightUpstreamAddress(o *bo.Options) (string, error) {
	if o.InfluxDB != nil && o.InfluxDB.FlightUpstreamAddress != "" {
		return o.InfluxDB.FlightUpstreamAddress, nil
	}
	u, err := url.Parse(o.OriginURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("no usable Flight SQL upstream address")
	}
	return u.Host, nil
}

func nativeBackend(c *config.Config, name string) (string, *bo.Options, error) {
	if c == nil {
		return "", nil, errors.New("missing configuration")
	}
	var found string
	var options *bo.Options
	for backendName, o := range c.Backends {
		if !o.UsesListener(name) {
			continue
		}
		if options != nil {
			return "", nil, errors.New("native listener requires exactly one backend")
		}
		if !strings.EqualFold(o.Provider, providers.InfluxDB) {
			return "", nil, errors.New("native listener requires an InfluxDB backend")
		}
		found, options = backendName, o
	}
	if options == nil {
		return "", nil, errors.New("no mapped backend")
	}
	return found, options, nil
}

func (nativeListenerAdapter) Describe(c *config.Config, name string) (native.Descriptor, error) {
	backendName, o, err := nativeBackend(c, name)
	if err != nil {
		return native.Descriptor{}, err
	}
	identity := o.Clone()
	identity.ListenerName = ""
	identity.ListenerNames = nil
	data, err := yamlencoding.Marshal(identity)
	if err != nil {
		return native.Descriptor{}, err
	}
	return native.Descriptor{RestartKey: fmt.Sprintf("%s:%x", backendName, sha256.Sum256(data))}, nil
}

func (a nativeListenerAdapter) Build(r native.BuildRequest) (listener.ProtocolServer, error) {
	backendName, o, err := nativeBackend(r.Config, r.ListenerName)
	if err != nil {
		return nil, err
	}
	descriptor, err := a.Describe(r.Config, r.ListenerName)
	if err != nil {
		return nil, err
	}
	upstream, err := flightUpstreamAddress(o)
	if err != nil {
		return nil, err
	}
	client, err := flight.NewFlightSQLClient(flight.UpstreamConfig{Address: upstream})
	if err != nil {
		return nil, err
	}
	backend := r.BackendClients.Get(backendName)
	if backend == nil {
		return nil, errors.New("missing InfluxDB backend client")
	}
	srv := flight.NewServer(client, newFlightCache(backend.Cache()))
	return flight.NewProtocolServer(srv, descriptor.RestartKey), nil
}
