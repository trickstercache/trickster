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

// Package greptimedb provides the GreptimeDB HTTP, PostgreSQL and MySQL backend.
package greptimedb

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/greptimedb/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DefaultPort is GreptimeDB's PostgreSQL wire-protocol port.
const DefaultPort = "4003"

// Client serves one GreptimeDB backend over HTTP and native SQL protocols.
type Client struct {
	*prometheus.Client
	sqlModeler *timeseries.Modeler
}

var (
	_ backends.Backend           = (*Client)(nil)
	_ backends.TimeseriesBackend = (*Client)(nil)
	_ types.NewBackendClientFunc = NewClient
)

// NewClient returns a GreptimeDB backend client.
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	c := &Client{sqlModeler: model.NewModeler()}
	b, err := prometheus.NewClientWithHooks(name, o, router, cache, promHooks())
	c.Client = b
	if err == nil {
		c.RegisterHandlers(nil)
	}
	return c, err
}

type engine struct{}

var (
	_ pgwire.Engine                = engine{}
	_ pgwire.HTTPEngine            = engine{}
	_ pgwire.SessionDefaultsEngine = engine{}
	_ pgwire.SessionSettingsEngine = engine{}
)

// Engine returns the GreptimeDB PostgreSQL wire-protocol engine.
func Engine() pgwire.Engine { return engine{} }

func (engine) Name() string                          { return providers.GreptimeDB }
func (engine) DefaultPort() string                   { return DefaultPort }
func (engine) Dialect() string                       { return providers.GreptimeDB }
func (engine) SupportsHTTP() bool                    { return true }
func (engine) Analyzer() sqlanalyzer.DialectAnalyzer { return analyzer }
func (engine) Defaults() pgwire.EngineDefaults {
	return pgwire.EngineDefaults{UpstreamTLSMode: pgo.TLSModeDisable}
}

func (engine) TimeAxis(oid uint32) (pgwire.TimeAxisKind, bool) {
	return pgwire.StandardTimeAxis(oid)
}
