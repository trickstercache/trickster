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

package engines_test

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse"
	"github.com/trickstercache/trickster/v2/pkg/backends/druid"
	"github.com/trickstercache/trickster/v2/pkg/backends/greptimedb"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb"
	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/postgres"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/vitess"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

// each directive, and what a statement carrying it in a comment must parse to
var conformanceDirectives = []struct {
	text string
	want timeseries.Directives
}{
	{"trickster-step-align:partial_end", timeseries.Directives{StepAlignment: timeseries.StepAlignmentPartialEnd}},
	{"trickster-volatile-window:90s", timeseries.Directives{VolatileWindow: 90 * time.Second}},
	{"trickster-volatile-window:30", timeseries.Directives{VolatileWindow: 30 * time.Second}},
	{"trickster-backfill-tolerance:45", timeseries.Directives{VolatileWindow: 45 * time.Second}},
	{"trickster-fast-forward:off", timeseries.Directives{FastForwardDisable: true}},
}

const conformanceFiller = "x"

// a provider path: its statement holds %s inside a string literal, and comment renders a directive in
// the language's comment syntax
type conformancePath struct {
	name, provider, statement string
	comment, literal          func(directive string) string
	request                   func(statement string) *http.Request
}

func sqlDashes(d string) string     { return "\n-- " + d }
func sqlBlock(d string) string      { return " /* " + d + " */" }
func literalDashes(d string) string { return "-- " + d }

func getWith(path, param string, extra url.Values) func(string) *http.Request {
	return func(statement string) *http.Request {
		v := url.Values{param: {statement}}
		maps.Copy(v, extra)
		return httptest.NewRequest(http.MethodGet, path+"?"+v.Encode(), nil)
	}
}

func postJSON(path string, body func(string) string) func(string) *http.Request {
	return func(statement string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body(statement)))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		return r
	}
}

func promRange() url.Values {
	now := time.Now().Truncate(time.Minute)
	return url.Values{
		"start": {strconv.FormatInt(now.Add(-6*time.Hour).Unix(), 10)},
		"end":   {strconv.FormatInt(now.Unix(), 10)}, "step": {"60"},
	}
}

var conformancePaths = []conformancePath{
	{
		name: "Prometheus", provider: providers.Prometheus,
		statement: `sum(rate(up{job="%s"}[5m]))`,
		comment:   func(d string) string { return " # " + d },
		literal:   func(d string) string { return "# " + d },
		request:   getWith("/api/v1/query_range", "query", promRange()),
	},
	{
		name: "ClickHouse", provider: providers.ClickHouse,
		statement: "SELECT toStartOfMinute(ts) AS t, count() AS cnt FROM e WHERE ts >= toDateTime(1516665600) " +
			"AND ts < toDateTime(1516687200) AND field2 = '%s' GROUP BY t ORDER BY t",
		comment: sqlBlock, literal: literalDashes,
		request: getWith("/", "query", nil),
	},
	{
		name: "InfluxQL", provider: providers.InfluxDB,
		statement: `SELECT mean("value") FROM "cpu" WHERE ("host" = '%s') AND time >= now() - 6h GROUP BY time(1m)`,
		comment:   sqlDashes, literal: literalDashes,
		request: getWith("/query", "q", url.Values{"db": {"db"}}),
	},
	{
		name: "Flux", provider: providers.InfluxDB,
		statement: "from(bucket: \"%s\")\n  |> range(start: -7d, stop: -6d)\n  |> aggregateWindow(every: 1m, func: mean)",
		comment:   func(d string) string { return "\n// " + d },
		literal:   func(d string) string { return "// " + d },
		request: func(statement string) *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/v2/query", strings.NewReader(statement))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationFlux)
			return r
		},
	},
	{
		name: "InfluxDB SQL", provider: providers.InfluxDB,
		statement: "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temperature) AS temperature FROM weather " +
			"WHERE location = '%s' AND time >= 1704067200 AND time < 1704153600 GROUP BY 1",
		comment: sqlDashes, literal: literalDashes,
		request: getWith("/api/v3/query_sql", "q", url.Values{"db": {"db"}}),
	},
	{
		name: "InfluxQL over v3", provider: providers.InfluxDB,
		statement: "SELECT mean(v) FROM cpu WHERE host = '%s' AND time >= '2024-01-01T00:00:00Z' " +
			"AND time < '2024-01-01T01:00:00Z' GROUP BY time(1m)",
		comment: sqlBlock, literal: literalDashes,
		request: getWith("/api/v3/query_influxql", "q", url.Values{"db": {"db"}, "format": {"json"}}),
	},
	{
		name: "Druid SQL", provider: providers.Druid,
		statement: "SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, SUM(v) AS value, host FROM foo " +
			"WHERE host = '%s' AND __time >= TIMESTAMP '2024-01-01 00:00:00' " +
			"AND __time < TIMESTAMP '2024-01-02 00:00:00' GROUP BY 1, host",
		comment: sqlDashes, literal: literalDashes,
		request: postJSON("/druid/v2/sql", func(s string) string {
			return `{"resultFormat":"object","query":` + strconv.Quote(s) + `}`
		}),
	},
	{
		name: "GreptimeDB SQL", provider: providers.GreptimeDB,
		statement: "SELECT date_bin('15m', ts) AS time, host, SUM(value) AS value FROM metrics WHERE host = '%s' " +
			"AND ts >= '2024-01-01T00:00:00Z' AND ts < '2024-01-01T01:00:00Z' GROUP BY 1,2 ORDER BY time,host",
		comment: sqlBlock, literal: literalDashes,
		request: getWith("/v1/sql", "sql", nil),
	},
	{
		name: "GreptimeDB PromQL", provider: providers.GreptimeDB,
		statement: `sum(rate(up{job="%s"}[5m]))`,
		comment:   func(d string) string { return " # " + d },
		literal:   func(d string) string { return "# " + d },
		request:   getWith("/v1/prometheus/api/v1/query_range", "query", promRange()),
	},
}

func conformanceClient(t *testing.T, provider string) (backends.TimeseriesBackend, *bo.Options) {
	t.Helper()
	o := bo.New()
	o.Provider, o.OriginURL = provider, "http://127.0.0.1:1"
	if err := o.Initialize(provider); err != nil {
		t.Fatal(err)
	}
	var (
		b   backends.Backend
		err error
	)
	switch provider {
	case providers.Prometheus:
		b, err = prometheus.NewClient(provider, o, nil, nil, nil, nil)
	case providers.ClickHouse:
		b, err = clickhouse.NewClient(provider, o, nil, nil, nil, nil)
	case providers.InfluxDB:
		b, err = influxdb.NewClient(provider, o, nil, nil, nil, nil)
	case providers.Druid:
		b, err = druid.NewClient(provider, o, nil, nil, nil, nil)
	case providers.GreptimeDB:
		b, err = greptimedb.NewClient(provider, o, nil, nil, nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.(backends.TimeseriesBackend), o
}

func parseForKey(t *testing.T, c backends.TimeseriesBackend, o *bo.Options,
	r *http.Request,
) (*timeseries.TimeRangeQuery, string) {
	t.Helper()
	pc := c.DefaultPathConfigs(o).Match(r.Method, r.URL.Path)
	if pc == nil {
		t.Fatalf("no path serves %s %s", r.Method, r.URL.Path)
	}
	rsc := request.NewResources(o, pc, nil, nil, nil, nil)
	r = request.SetResources(r, rsc)
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil || trq == nil {
		t.Fatalf("%s was not planned: %v", r.URL, err)
	}
	rsc.TimeRangeQuery = trq
	return trq, engines.DeriveCacheKeyForRequest(r, "")
}

func TestEveryProviderReadsDirectivesFromCommentsOnly(t *testing.T) {
	for _, path := range conformancePaths {
		t.Run(path.name, func(t *testing.T) {
			c, o := conformanceClient(t, path.provider)
			plain := fmt.Sprintf(path.statement, conformanceFiller)
			plainQuery, plainKey := parseForKey(t, c, o, path.request(plain))
			if !plainQuery.Directives.IsZero() {
				t.Fatalf("a statement without directives read %+v", plainQuery.Directives)
			}
			for _, d := range conformanceDirectives {
				commented, key := parseForKey(t, c, o, path.request(plain+path.comment(d.text)))
				if commented.Directives != d.want {
					t.Errorf("%s in a comment read as %+v", d.text, commented.Directives)
				}
				// a directive changes how a query is cached and served, never its cache identity
				if key != plainKey {
					t.Errorf("%s changed the cache key", d.text)
				}
				literal, _ := parseForKey(t, c, o, path.request(fmt.Sprintf(path.statement, path.literal(d.text))))
				if !literal.Directives.IsZero() {
					t.Errorf("%s inside a string literal read as %+v", d.text, literal.Directives)
				}
			}
		})
	}
}

func TestDruidNativeReadsDirectivesFromItsContext(t *testing.T) {
	c, o := conformanceClient(t, providers.Druid)
	query := func(context string) *http.Request {
		body := `{"queryType":"timeseries","dataSource":"wiki","intervals":["2024-01-01T00:00:00Z/2024-01-02T00:00:00Z"],` +
			`"granularity":"hour","aggregations":[{"type":"count","name":"count"}]` + context + `}`
		r := httptest.NewRequest(http.MethodPost, "/druid/v2", strings.NewReader(body))
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
		return r
	}
	plainQuery, plainKey := parseForKey(t, c, o, query(""))
	if !plainQuery.Directives.IsZero() {
		t.Fatalf("a query without directives read %+v", plainQuery.Directives)
	}
	for _, test := range []struct {
		name, context string
		want          timeseries.Directives
	}{
		{"a string", `"trickster-step-align":"partial_end"`, conformanceDirectives[0].want},
		{"a duration", `"trickster-volatile-window":"90s"`, conformanceDirectives[1].want},
		{"a number of seconds", `"trickster-volatile-window":30`, conformanceDirectives[2].want},
		{"the fallback window", `"trickster-backfill-tolerance":45`, conformanceDirectives[3].want},
		{
			"a volatile window with the fallback", `"trickster-backfill-tolerance":45,"trickster-volatile-window":30`,
			conformanceDirectives[2].want,
		},
		{"fast forward", `"trickster-fast-forward":"off"`, timeseries.Directives{FastForwardDisable: true}},
		{"a value no directive takes", `"trickster-step-align":true`, timeseries.Directives{}},
		// a directive's text as another key's value is not a directive
		{"a value", `"queryId":"trickster-step-align:partial_end"`, timeseries.Directives{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			trq, key := parseForKey(t, c, o, query(`,"context":{`+test.context+`}`))
			if trq.Directives != test.want {
				t.Errorf("read %+v", trq.Directives)
			}
			if key != plainKey {
				t.Error("a directive changed the cache key")
			}
		})
	}
}

func TestDruidSQLCombinesCommentsAndContext(t *testing.T) {
	// a comment wins over the context for the same name, and trickster-volatile-window from either
	// outranks the fallback from either
	c, o := conformanceClient(t, providers.Druid)
	window := func(d time.Duration) timeseries.Directives { return timeseries.Directives{VolatileWindow: d} }
	for _, test := range []struct {
		name, comment, context string
		want                   timeseries.Directives
	}{
		{
			"a comment's mode over the context's", " -- trickster-step-align:drop", `"trickster-step-align":"partial",` +
				`"trickster-volatile-window":"2m"`,
			timeseries.Directives{StepAlignment: timeseries.StepAlignmentDrop, VolatileWindow: 2 * time.Minute},
		},
		{
			"a context window over a comment's fallback", " -- trickster-backfill-tolerance:45",
			`"trickster-volatile-window":30`, window(30 * time.Second),
		},
		{
			"a comment's window over a context fallback", " -- trickster-volatile-window:30",
			`"trickster-backfill-tolerance":45`, window(30 * time.Second),
		},
		{
			"a comment's fallback over the context's", " -- trickster-backfill-tolerance:45",
			`"trickster-backfill-tolerance":60`, window(45 * time.Second),
		},
		{
			"a comment's window over the context's", " -- trickster-volatile-window:30",
			`"trickster-volatile-window":60`, window(30 * time.Second),
		},
		{"a context fallback alone", "", `"trickster-backfill-tolerance":45`, window(45 * time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := fmt.Sprintf(conformancePaths[6].statement, conformanceFiller) + test.comment
			body := `{"query":` + strconv.Quote(statement) + `,"context":{` + test.context + `}}`
			r := httptest.NewRequest(http.MethodPost, "/druid/v2/sql", strings.NewReader(body))
			r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			trq, _ := parseForKey(t, c, o, r)
			require.Equal(t, test.want, trq.Directives)
		})
	}
}

func TestEveryNativeAnalyzerReadsDirectivesFromCommentsOnly(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	postgresSession := postgres.Engine().Analyzer().(pgwire.SessionAnalyzer).ForSession(pgwire.SessionView{UTC: true})
	greptimeSession := greptimedb.Engine().Analyzer().(pgwire.SessionAnalyzer).ForSession(pgwire.SessionView{UTC: true})
	for _, test := range []struct {
		name      string
		analyzer  sqlanalyzer.DialectAnalyzer
		statement string
		comment   func(string) string
	}{
		{
			"MySQL", vitess.MustNewAnalyzer(),
			"SELECT UNIX_TIMESTAMP(ts) DIV 300 * 300 AS time, cab_type AS metric, COUNT(*) AS trips FROM trips " +
				"WHERE ts >= FROM_UNIXTIME(1785542400) AND ts < FROM_UNIXTIME(1785628800) AND note = '%s' " +
				"GROUP BY time, cab_type ORDER BY time, metric",
			func(d string) string { return " # " + d },
		},
		{
			"PostgreSQL", postgresSession,
			"SELECT time_bucket('300.000s', ts) AS time, count(*) FROM trips WHERE note = '%s' AND " +
				"ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z' GROUP BY 1 ORDER BY 1",
			sqlDashes,
		},
		{
			"Flight SQL", cockroach.NewAnalyzer(cockroach.Options{BucketMatchers: cockroach.DataFusionBucketMatchers()}),
			"SELECT date_bin(INTERVAL '1 minute', time) AS time, host, avg(v) AS v FROM m WHERE note = '%s' AND " +
				"time >= 0 AND time < 600 GROUP BY 1, host",
			sqlBlock,
		},
		{
			"GreptimeDB MySQL", greptimedb.MySQLEngine().Analyzer(mysql.SessionView{TimeZone: "UTC"}),
			"SELECT DATE_BIN('1m', ts, FROM_UNIXTIME(0)) AS time, label, COUNT(*) AS value FROM readings " +
				"WHERE note = '%s' AND ts >= FROM_UNIXTIME(1767225600) AND ts < FROM_UNIXTIME(1767225720) " +
				"GROUP BY time, label ORDER BY time, label",
			sqlBlock,
		},
		{
			"GreptimeDB PostgreSQL", greptimeSession,
			"SELECT date_bin('5m', pickup_datetime) AS time, count(*) FROM trips WHERE note = '%s' AND " +
				"pickup_datetime >= '2026-01-01T00:00:00Z' AND pickup_datetime < '2026-02-01T00:00:00Z' " +
				"GROUP BY 1 ORDER BY 1",
			sqlDashes,
		},
		{
			"ClickHouse", aftership.NewAnalyzer(aftership.Options{}),
			"SELECT (intDiv(toUInt32(ts), 300) * 300) * 1000 AS t, count() FROM events WHERE note = '%s' AND " +
				"t >= 1516665600000 AND t < 1516687200000 GROUP BY t",
			// ClickHouse takes # comments, but the parser Trickster reads it with doesn't
			sqlDashes,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := func(statement string) *sqlanalyzer.QueryPlan {
				t.Helper()
				analysis := test.analyzer.Analyze(statement, now)
				if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
					t.Fatalf("%q was not delta planned: %s (%v)", statement, analysis.Reason, analysis.Err)
				}
				return analysis.Plan
			}
			plain := plan(fmt.Sprintf(test.statement, conformanceFiller))
			for _, d := range conformanceDirectives {
				commented := plan(fmt.Sprintf(test.statement, conformanceFiller) + test.comment(d.text))
				if commented.Directives != d.want {
					t.Errorf("%s in a comment read as %+v", d.text, commented.Directives)
				}
				// the native listeners key a plan on its canonical statement
				if commented.CanonicalSQL != plain.CanonicalSQL {
					t.Errorf("%s changed the canonical statement", d.text)
				}
				if literal := plan(fmt.Sprintf(test.statement, literalDashes(d.text))); !literal.Directives.IsZero() {
					t.Errorf("%s inside a string literal read as %+v", d.text, literal.Directives)
				}
			}
		})
	}
}

func TestPrometheusDirectivesKeepTheKeyOfTheirQuery(t *testing.T) {
	// wherever a directive sits, the statement keys as the one written without it, sent either way
	c, o := conformanceClient(t, providers.Prometheus)
	request := func(post bool, query string) *http.Request {
		v := promRange()
		v.Set("query", query)
		if !post {
			return httptest.NewRequest(http.MethodGet, "/api/v1/query_range?"+v.Encode(), nil)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/query_range", strings.NewReader(v.Encode()))
		r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
		return r
	}
	for _, post := range []bool{false, true} {
		_, plain := parseForKey(t, c, o, request(post, "up"))
		for _, query := range []string{
			"# trickster-step-align:drop\nup",
			"up # trickster-step-align:drop",
			"up\n# trickster-step-align:drop",
			"  # trickster-step-align:drop\n\t# trickster-volatile-window:90s\nup",
		} {
			trq, key := parseForKey(t, c, o, request(post, query))
			require.False(t, trq.Directives.IsZero(), "%q", query)
			require.Equal(t, plain, key, "post %t: %q keyed apart from up", post, query)
		}
	}
}

func TestPostgreSQLEscapeStringsHoldNoDirectives(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	a := postgres.Engine().Analyzer().(pgwire.SessionAnalyzer).ForSession(pgwire.SessionView{UTC: true})
	const statement = "SELECT time_bucket('300.000s', ts) AS time, count(*) FROM trips WHERE " +
		"note = E'it\\'s -- trickster-step-align:off' AND ts >= '2026-09-18T08:00:00Z' AND " +
		"ts < '2026-09-18T11:00:00Z' GROUP BY 1 ORDER BY 1"
	for _, test := range []struct {
		name, statement string
		want            timeseries.Directives
	}{
		{"directive text inside the literal", statement, timeseries.Directives{}},
		{
			"a comment after the literal", statement + " -- trickster-step-align:drop",
			timeseries.Directives{StepAlignment: timeseries.StepAlignmentDrop},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			analysis := a.Analyze(test.statement, now)
			require.Equal(t, sqlanalyzer.CacheModeDelta, analysis.Mode, "%s: %v", analysis.Reason, analysis.Err)
			require.Equal(t, test.want, analysis.Plan.Directives)
		})
	}
}
