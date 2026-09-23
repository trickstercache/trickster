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

package greptimedb

import (
	"errors"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/vitess"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/sqlparser"
)

type mysqlEngine struct{}

var mysqlAnalyzer = newMySQLAnalyzer()

// MySQLEngine returns Greptime's MySQL wire-protocol implementation.
func MySQLEngine() mysql.Engine { return mysqlEngine{} }

func (mysqlEngine) Name() string        { return providers.GreptimeDB }
func (mysqlEngine) DefaultPort() string { return "4002" }
func (mysqlEngine) SupportsHTTP() bool  { return true }
func (mysqlEngine) Analyzer(view mysql.SessionView) sqlanalyzer.DialectAnalyzer {
	return &mysqlDialectAnalyzer{inner: mysqlAnalyzer, utc: view.TimeZone == "UTC" || view.TimeZone == "+00:00"}
}

func (mysqlEngine) StreamState(conn *vtmysql.Conn) (uint16, uint16, error) {
	if conn == nil {
		return 0, 0, errors.New("nil GreptimeDB MySQL connection")
	}
	// Greptime's record-batch writer ends resultsets with default EOF status
	// and warning fields. Its diagnostic SQL is different from MySQL's and
	// must not run here: even SHOW COUNT(*) WARNINGS is unsupported. Statements
	// returning OK packets retain their actual metadata in the shared path.
	if result := conn.StreamOKResult(); result != nil {
		return result.StatusFlags, 0, nil
	}
	return 0, 0, nil
}

func (mysqlEngine) InitSession(conn *vtmysql.Conn) (mysql.SessionView, error) {
	if conn == nil {
		return mysql.SessionView{}, errors.New("nil GreptimeDB MySQL connection")
	}
	r, err := conn.ExecuteFetch("SHOW TIMEZONE", 1, true)
	if err != nil {
		return mysql.SessionView{}, err
	}
	if r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].IsNull() {
		return mysql.SessionView{}, errors.New("invalid GreptimeDB time zone")
	}
	return mysql.SessionView{TimeZone: r.Rows[0][0].ToString()}, nil
}

func (mysqlEngine) ResultSemantics() mysql.ResultSemantics {
	return mysql.ResultSemantics{Timestamp: mysqlTimestampEpoch, BinaryText: true, NullsLast: true}
}

func mysqlTimestampEpoch(value sqltypes.Value) (int64, error) {
	t, err := time.Parse("2006-01-02 15:04:05.999999999", value.ToString())
	if err != nil {
		return 0, err
	}
	if !sqlanalyzer.SafeUnixSeconds(t.Unix()) {
		return 0, errors.New("GreptimeDB timestamp is outside the cache range")
	}
	return t.UnixNano(), nil
}

func (c *Client) MySQLRouteConfig() (mysql.ProtocolConfig, error) {
	return mysql.ProtocolConfigForEngine(c.Configuration(), MySQLEngine())
}

type mysqlDialectAnalyzer struct {
	inner *vitess.Analyzer
	utc   bool
}

func newMySQLAnalyzer() *vitess.Analyzer {
	a, err := vitess.NewAnalyzerWithOptions(vitess.Options{
		BucketMatchers:         []vitess.BucketMatcher{mysqlTimestampBucket},
		DeterministicFunctions: []string{"date_bin", "date_trunc"},
	})
	if err != nil {
		panic(err)
	}
	return a
}

func (a *mysqlDialectAnalyzer) Analyze(query string, _ time.Time) sqlanalyzer.Analysis {
	stmt, err := a.inner.Parser().Parse(query)
	return a.AnalyzeParsed(query, stmt, err)
}

func (a *mysqlDialectAnalyzer) AnalyzeParsed(query string, stmt sqlparser.Statement, err error) sqlanalyzer.Analysis {
	analysis := a.inner.AnalyzeParsed(query, stmt, err)
	if analysis.Mode != sqlanalyzer.CacheModeDelta {
		return analysis
	}
	p := analysis.Plan
	// MySQL's integer division, epoch inference and implicit casts are not
	// Greptime contracts. Timestamp bucket results are lossless in UTC only.
	if !a.utc || p.OutputUnit != timeseries.DateTimeSQL || p.InputUnit != timeseries.DateTimeSQL ||
		p.Step%time.Second != 0 ||
		p.LowerBound.Value.UnixNano()%int64(p.Step) != 0 || p.UpperBound.Value.UnixNano()%int64(p.Step) != 0 {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, errUnrenderable)
	}
	unsafe := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.BetweenExpr:
			unsafe = true
		case *sqlparser.ComparisonExpr:
			if n.Operator == sqlparser.LessEqualOp {
				unsafe = true
			}
		}
		return !unsafe, nil
	}, stmt)
	if unsafe {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsafePredicate, errUnrenderable)
	}
	return analysis
}

func mysqlTimestampBucket(expr sqlparser.Expr) (*sqlparser.ColName, time.Duration, timeseries.FieldDataType, bool) {
	fn, ok := expr.(*sqlparser.FuncExpr)
	if !ok || !fn.Qualifier.IsEmpty() {
		return nil, 0, 0, false
	}
	name := strings.ToLower(fn.Name.String())
	if (name != "date_bin" || len(fn.Exprs) != 3) && (name != "date_trunc" || len(fn.Exprs) != 2) {
		return nil, 0, 0, false
	}
	width, ok := fn.Exprs[0].(*sqlparser.Literal)
	if !ok || width.Type != sqlparser.StrVal {
		return nil, 0, 0, false
	}
	column, ok := fn.Exprs[1].(*sqlparser.ColName)
	if !ok {
		return nil, 0, 0, false
	}
	var step time.Duration
	if name == "date_bin" {
		anchor, ok := fn.Exprs[2].(*sqlparser.FuncExpr)
		if !ok || !anchor.Qualifier.IsEmpty() || !strings.EqualFold(anchor.Name.String(), "from_unixtime") || len(anchor.Exprs) != 1 {
			return nil, 0, 0, false
		}
		zero, ok := anchor.Exprs[0].(*sqlparser.Literal)
		if !ok || zero.Type != sqlparser.IntVal || zero.Val != "0" {
			return nil, 0, 0, false
		}
		step, _ = cockroach.ParseCompactDuration(width.Val, compactUnits)
	} else {
		step = map[string]time.Duration{"second": time.Second, "minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour}[strings.ToLower(width.Val)]
	}
	return column, step, timeseries.DateTimeSQL, step > 0
}
