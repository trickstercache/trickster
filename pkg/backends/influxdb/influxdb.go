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

// Package influxdb provides the InfluxDB Backend provider
package influxdb

import (
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flight"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers,
		router, cache, NewModeler())
	c.TimeseriesBackend = b
	if err == nil && o != nil && o.FlightPort > 0 {
		if ferr := startFlightListener(name, o); ferr != nil {
			logger.Error("flight sql listener startup failed",
				logging.Pairs{"backend": name, "port": o.FlightPort, "detail": ferr})
		}
	}
	return c, err
}

// startFlightListener launches a Flight SQL server on o.FlightPort that
// proxies queries to the backend's upstream Flight SQL endpoint. The listener
// is registered in the flight package's registry; the daemon drains it on
// SIGTERM via flight.ShutdownAll, and calls with a reused Name replace the
// existing listener (supporting config reload).
func startFlightListener(name string, o *bo.Options) error {
	upstream := o.FlightUpstreamAddress
	if upstream == "" {
		u, err := url.Parse(o.OriginURL)
		if err != nil {
			return err
		}
		upstream = u.Host
	}
	fc, err := flight.NewFlightSQLClient(flight.UpstreamConfig{
		Address: upstream,
	})
	if err != nil {
		return err
	}
	srv := flight.NewServer(fc, nil)
	lis, err := flight.Start(flight.ListenerConfig{
		Address: "0.0.0.0",
		Port:    o.FlightPort,
		Name:    name,
	}, srv)
	if err != nil {
		return err
	}
	logger.Info("flight sql listener started",
		logging.Pairs{"backend": name, "address": lis.Addr().String()})
	return nil
}
