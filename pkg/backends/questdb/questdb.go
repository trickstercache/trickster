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

// Package questdb provides the QuestDB HTTP and PostgreSQL wire backend.
package questdb

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DefaultPort is QuestDB's PostgreSQL wire-protocol port.
const DefaultPort = "8812"

// Client serves QuestDB's HTTP surface and PostgreSQL wire protocol.
type Client struct {
	backends.Backend
}

var (
	_ backends.Backend           = (*Client)(nil)
	_ types.NewBackendClientFunc = NewClient
)

// NewClient returns a QuestDB backend client for the HTTP and pgwire surfaces.
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, c.RegisterHandlers, router, nil)
	c.Backend = b
	return c, err
}

// StepAlignments returns the SQL step-alignment modes supported by QuestDB's
// fixed-width time-series queries.
func (c *Client) StepAlignments() (supported, def timeseries.StepAlignment) {
	return sqlanalyzer.StepAlignments, sqlanalyzer.DefaultStepAlignment
}

type engine struct{}

var (
	_ pgwire.Engine                = engine{}
	_ pgwire.HTTPEngine            = engine{}
	_ pgwire.SessionDefaultsEngine = engine{}
	_ pgwire.SessionSettingsEngine = engine{}
)

// Engine returns the QuestDB PostgreSQL wire-protocol engine.
func Engine() pgwire.Engine { return engine{} }

func (engine) Name() string        { return providers.QuestDB }
func (engine) DefaultPort() string { return DefaultPort }
func (engine) Dialect() string     { return providers.QuestDB }

func (engine) Analyzer() sqlanalyzer.DialectAnalyzer { return analyzer }

func (engine) Defaults() pgwire.EngineDefaults {
	return pgwire.EngineDefaults{UpstreamTLSMode: pgo.TLSModeDisable}
}

func (engine) TimeAxis(oid uint32) (pgwire.TimeAxisKind, bool) {
	return pgwire.StandardTimeAxis(oid)
}

func (engine) TimeSemantics() pgwire.TimeSemantics {
	return pgwire.TimeSemantics{
		NaiveTimestampsAreUTC: true,
		AssumedDateStyle:      "ISO",
	}
}

func (engine) SessionDefaultsProbe() pgwire.SessionDefaultsProbe {
	return pgwire.SessionDefaultsProbe{
		SQL:   "SELECT current_setting('extra_float_digits'), current_setting('bytea_output')",
		Names: []string{"extra_float_digits", "bytea_output"},
	}
}

func (engine) SessionSettings() pgwire.SessionSettings {
	return pgwire.SessionSettings{
		Tracked: map[string]struct{}{
			"timezone": {}, "datestyle": {}, "intervalstyle": {},
			"extra_float_digits": {}, "bytea_output": {}, "search_path": {},
		},
		Neutral: map[string]struct{}{
			"application_name": {}, "statement_timeout": {}, "lock_timeout": {},
			"idle_in_transaction_session_timeout": {}, "idle_session_timeout": {},
			"work_mem": {}, "jit": {},
		},
		Aliases:            map[string]string{"time_zone": "timezone"},
		UnconfirmedStartup: true,
	}
}

func (engine) SupportsHTTP() bool { return true }
