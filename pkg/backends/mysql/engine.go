/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mysql

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/sqlparser"
)

// SessionView is the connection-scoped state visible to dialect analysis.
type SessionView struct {
	TimeZone string
}

// Engine supplies a provider's SQL analysis and result-state contract while
// sharing the MySQL listener, authentication, limits and cache transport.
type Engine interface {
	Name() string
	DefaultPort() string
	SupportsHTTP() bool
	Analyzer(SessionView) sqlanalyzer.DialectAnalyzer
	StreamState(*vtmysql.Conn) (status, warnings uint16, err error)
}

// SessionInitializer reads effective defaults without modifying the origin session.
type SessionInitializer interface {
	InitSession(*vtmysql.Conn) (SessionView, error)
}

// ResultSemantics supplies the ordering and timestamp rules of a compatible
// engine. Zero values keep MySQL's result rules.
type ResultSemantics struct {
	Timestamp  func(sqltypes.Value) (int64, error)
	BinaryText bool
	NullsLast  bool
}

type resultEngine interface{ ResultSemantics() ResultSemantics }

func (h *protocolHandler) resultSemantics() ResultSemantics {
	if e, ok := h.config.Engine.(resultEngine); ok {
		return e.ResultSemantics()
	}
	return ResultSemantics{}
}

func (h *protocolHandler) resultEpoch(value sqltypes.Value, unit timeseries.FieldDataType) (int64, error) {
	if unit == timeseries.DateTimeSQL {
		if parser := h.resultSemantics().Timestamp; parser != nil {
			return parser(value)
		}
	}
	return resultEpoch(value, unit)
}

// ProtocolConfigForEngine derives a native endpoint without mutating the HTTP
// origin or the caller's options. A nil engine retains the MySQL defaults.
func ProtocolConfigForEngine(o *bo.Options, engine Engine) (ProtocolConfig, error) {
	if engine == nil {
		return ProtocolConfigFromOptions(o)
	}
	copy, err := upstreamOptionsForEngine(o, engine)
	if err != nil {
		return ProtocolConfig{}, err
	}
	config, err := ProtocolConfigFromOptions(copy)
	if err != nil {
		return ProtocolConfig{}, err
	}
	config.Engine = engine
	config.RestartKey = engine.Name() + ":" + config.RestartKey
	return config, nil
}

func upstreamOptionsForEngine(o *bo.Options, engine Engine) (*bo.Options, error) {
	if o == nil {
		return nil, errors.New("nil MySQL backend options")
	}
	if engine == nil {
		return o, nil
	}
	copy := o.Clone()
	raw := copy.OriginURL
	override := copy.MySQL != nil && copy.MySQL.UpstreamURL != ""
	if override {
		raw = copy.MySQL.UpstreamURL
		copy.MySQL.UpstreamURL = ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid MySQL upstream URL")
	}
	if u.Hostname() == "" {
		return nil, errors.New("MySQL upstream URL has no host")
	}
	if !override && engine.SupportsHTTP() && (u.Scheme == "http" || u.Scheme == "https") {
		// HTTP credentials must not cross into a native protocol implicitly.
		u = &url.URL{Scheme: "mysql", Host: net.JoinHostPort(u.Hostname(), engine.DefaultPort())}
	} else if u.Scheme == "mysql" && u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), engine.DefaultPort())
	}
	if u.Scheme == "mysql" {
		if port, err := strconv.ParseUint(u.Port(), 10, 16); err != nil || port == 0 {
			return nil, errors.New("invalid MySQL upstream port")
		}
	}
	copy.OriginURL = u.String()
	return copy, nil
}

func (h *protocolHandler) originProtocolState(upstream *vtmysql.Conn) (uint16, uint16, error) {
	if h.config.Engine != nil {
		return h.config.Engine.StreamState(upstream)
	}
	return originProtocolState(upstream)
}

func (h *protocolHandler) analyzeQuery(query string, parsed parsedQuery, session *upstreamSession, now time.Time) sqlanalyzer.Analysis {
	var analyzer sqlanalyzer.DialectAnalyzer = defaultAnalyzer
	if h.config.Engine != nil {
		session.mtx.Lock()
		view := session.viewLocked()
		session.mtx.Unlock()
		analyzer = h.config.Engine.Analyzer(view)
	}
	if analyzer == nil {
		return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonUnsupportedStatement}
	}
	if a, ok := analyzer.(interface {
		AnalyzeParsed(string, sqlparser.Statement, error) sqlanalyzer.Analysis
	}); ok {
		return a.AnalyzeParsed(query, parsed.statement, parsed.err)
	}
	return analyzer.Analyze(query, now)
}

func (session *upstreamSession) viewLocked() SessionView {
	zone := session.timeZone
	if zone == "" {
		zone = session.defaultTimeZone
	}
	return SessionView{TimeZone: zone}
}

func (h *protocolHandler) dialect() string {
	if h.config.Engine != nil {
		return h.config.Engine.Name()
	}
	return mysqlDialect
}
