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

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	offEngine = "ObjectProxyCache"
	// 7s and 13s past a minute sit off the 10s, 15s, 1m and 5m grids the queries below bucket on
	offStartSkew = 7 * time.Second
	offEndSkew   = 13 * time.Second
	offShift     = time.Second
	// Telegraf's data reaches back only to the environment's start, so its ranges are recent and short
	recentSpan   = 3 * time.Minute
	recentSettle = 2 * time.Minute

	offPromAddr       = "127.0.0.1:9090"
	offClickHouseAddr = "127.0.0.1:8123"
	offDruidAddr      = "127.0.0.1:8888"
	offInfluxDB2Addr  = "127.0.0.1:8086"
	offInfluxDB3Addr  = "127.0.0.1:8181"
	offMySQLAddr      = "127.0.0.1:3306"

	offPromBackend       = "prom1"
	offClickHouseBackend = "click1"
	offDruidBackend      = "druid1"
	offInfluxBackend     = "flux1"
	offPGBackend         = "timescaledb1"
	offPGAuth            = "timescaledb-grafana"
	offFlightBackend     = "influx3"
	offFlightName        = "influx3-flight"
	offMySQLBackend      = "mysql1"
	offLocalhost         = "127.0.0.1"
	offInfluxDB          = "trickster"
	offInfluxUser        = "trickster"
	offInfluxTokenVal    = "trickster-dev-token"
	offInfluxToken       = "Token " + offInfluxTokenVal
	offModeObject        = "object"
	offModeDelta         = "delta"
	// scraped series hold only what Prometheus scraped since starting, but the trips series are backfilled
	offPromQuery     = "sum(trips_in_progress)"
	offSQLCacheTotal = "trickster_sql_query_cache_total"
	// the dev env's ladder for dev.fast.*, so graphite delta-caches those leaves without learning them first
	offGraphiteFast       = `^dev\.fast\.`
	offGraphiteRetentions = "10s:6h,60s:7d,10m:5y"

	offClickHouseSQL = "SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt FROM trips " +
		"WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) GROUP BY t ORDER BY t FORMAT JSON"
	offDruidNative = `{"queryType":"timeseries","dataSource":"trips","granularity":"five_minute",` +
		`"intervals":["%s/%s"],"aggregations":[{"type":"count","name":"trips"}]}`
	offDruidSQL = `{"query":"SELECT TIME_FLOOR(__time, 'PT5M') AS bucket, COUNT(*) AS trips FROM trips ` +
		`WHERE __time >= MILLIS_TO_TIMESTAMP(%d) AND __time < MILLIS_TO_TIMESTAMP(%d) GROUP BY 1 ORDER BY 1"}`
	offInfluxQL = `SELECT mean("usage_idle") FROM "cpu" WHERE "cpu" = 'cpu-total' ` +
		`AND time >= '%s' AND time < '%s' GROUP BY time(10s)`
	offFlux = `from(bucket: "trickster") |> range(start: %s, stop: %s) |> filter(fn: (r) => ` +
		`r._measurement == "cpu" and r._field == "usage_idle" and r.cpu == "cpu-total") |> aggregateWindow(every: 1m, fn: mean)`
	offInflux3SQL = "SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) AS usage_idle FROM cpu " +
		"WHERE cpu = 'cpu-total' AND time >= '%s' AND time < '%s' GROUP BY 1 ORDER BY 1"
	offMySQLSQL = "SELECT UNIX_TIMESTAMP(pickup_datetime) DIV 300 * 300 AS time, COUNT(*) AS value FROM trips " +
		"WHERE pickup_datetime >= FROM_UNIXTIME(%d) AND pickup_datetime < FROM_UNIXTIME(%d) GROUP BY time ORDER BY time"
)

var offBackends = []string{
	offPromBackend, graphiteBackend, offClickHouseBackend, offDruidBackend, offInfluxBackend, offFlightBackend, offMySQLBackend, offPGBackend,
}

type offHTTPCase struct {
	name, backend, path, origin string
	from, to                    time.Time
	opts                        func(from, to time.Time) []requestOption
	ignore                      []string
}

func TestStepAlignmentOff(t *testing.T) {
	waitForPrometheusData(t, offPromAddr)
	waitForClickHouseData(t, offClickHouseAddr)
	waitForGraphiteData(t, graphiteWebAddr)
	waitForInfluxDB3Data(t, offInfluxDB3Addr)
	latest := waitForInfluxDBData(t, offInfluxDB2Addr)
	h, pgAddr, flightAddr := stepAlignmentOffHarness(t)
	h.start(t)

	now := time.Now().UTC()
	promFrom, promTo := offRange(now.Add(-2*time.Hour), 30*time.Minute)
	tripsFrom, tripsTo := offRange(now.Add(-26*time.Hour), time.Hour)
	fluxFrom, fluxTo := recentRange(latest)
	i3From, i3To := recentRange(now)
	waitForInfluxDBHistory(t, offInfluxDB2Addr, fluxFrom)
	waitForInfluxDB3History(t, offInfluxDB3Addr, i3From)
	// unaligned native intervals take the object lane under any mode, so aligned ones show off bypassing the delta cache
	druidFrom := now.Add(-26 * time.Hour).Truncate(time.Hour)
	unix := func(ts time.Time) string { return strconv.FormatInt(ts.Unix(), 10) }
	clickHouse := func(from, to time.Time) string { return fmt.Sprintf(offClickHouseSQL, from.Unix(), to.Unix()) }
	influxQL := func(from, to time.Time, extra url.Values) url.Values {
		extra.Set("db", offInfluxDB)
		extra.Set("q", fmt.Sprintf(offInfluxQL, from.Format(time.RFC3339), to.Format(time.RFC3339)))
		return extra
	}

	for _, tc := range []offHTTPCase{
		{
			name: "prometheus", backend: offPromBackend, path: "/api/v1/query_range", origin: offPromAddr,
			from: promFrom, to: promTo, opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{
					"query": {offPromQuery}, "start": {unix(from)}, "end": {unix(to)}, "step": {"15"},
				})}
			},
		},
		{
			name: "graphite multi-target", backend: graphiteBackend, path: "/render", origin: graphiteWebAddr,
			from: promFrom, to: promTo, opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{
					"target": {fastLeaves[0], fastLeaves[1]}, "from": {unix(from)}, "until": {unix(to)},
					"format": {"json"},
				})}
			},
		},
		{
			name: "clickhouse GET", backend: offClickHouseBackend, path: "/", origin: offClickHouseAddr,
			from: tripsFrom, to: tripsTo, ignore: []string{"statistics"},
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{"query": {clickHouse(from, to)}})}
			},
		},
		{
			name: "clickhouse POST", backend: offClickHouseBackend, path: "/", origin: offClickHouseAddr,
			from: tripsFrom, to: tripsTo, ignore: []string{"statistics"},
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withBody(headers.ValueTextPlain, clickHouse(from, to))}
			},
		},
		{
			name: "druid native", backend: offDruidBackend, path: "/druid/v2", origin: offDruidAddr,
			from: druidFrom, to: druidFrom.Add(time.Hour), opts: func(from, to time.Time) []requestOption {
				return []requestOption{withBody(headers.ValueApplicationJSON,
					fmt.Sprintf(offDruidNative, from.Format(time.RFC3339), to.Format(time.RFC3339)))}
			},
		},
		{
			name: "druid sql", backend: offDruidBackend, path: "/druid/v2/sql", origin: offDruidAddr,
			from: tripsFrom, to: tripsTo, opts: func(from, to time.Time) []requestOption {
				return []requestOption{withBody(headers.ValueApplicationJSON,
					fmt.Sprintf(offDruidSQL, from.UnixMilli(), to.UnixMilli()))}
			},
		},
		{
			name: "influxql", backend: offInfluxBackend, path: "/query", origin: offInfluxDB2Addr,
			from: fluxFrom, to: fluxTo, opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(influxQL(from, to, url.Values{
					"u": {offInfluxUser}, "p": {offInfluxTokenVal},
				}))}
			},
		},
		{
			name: "influxql authorization header", backend: offInfluxBackend, path: "/query", origin: offInfluxDB2Addr,
			from: fluxFrom, to: fluxTo, opts: func(from, to time.Time) []requestOption {
				return []requestOption{
					withHeader(headers.NameAuthorization, offInfluxToken),
					withParams(influxQL(from, to, url.Values{})),
				}
			},
		},
		{
			name: "flux", backend: offInfluxBackend, path: "/api/v2/query", origin: offInfluxDB2Addr,
			from: fluxFrom, to: fluxTo, opts: func(from, to time.Time) []requestOption {
				query := fmt.Sprintf(offFlux, from.Format(time.RFC3339), to.Format(time.RFC3339))
				return []requestOption{
					withHeader(headers.NameAuthorization, offInfluxToken),
					withParams(url.Values{"org": {"trickster-dev"}}),
					withBody(headers.ValueApplicationJSON, fmt.Sprintf(`{"query": %q, "type": "flux"}`, query)),
				}
			},
		},
		{
			name: "influxdb3 sql", backend: offFlightBackend, path: "/api/v3/query_sql", origin: offInfluxDB3Addr,
			from: i3From, to: i3To, opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{
					"db": {offInfluxDB}, "format": {"json"},
					"q": {fmt.Sprintf(offInflux3SQL, from.Format(time.RFC3339), to.Format(time.RFC3339))},
				})}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send := func(to time.Time) (http.Header, []byte) {
				t.Helper()
				resp, body := h.do(t, "/"+tc.backend+tc.path, tc.opts(tc.from, to)...)
				require.Equal(t, http.StatusOK, resp.StatusCode, "body: %.240s", body)
				return resp.Header, body
			}
			hdr, first := send(tc.to)
			requireTricksterResult(t, hdr, map[string]string{keys.Engine: offEngine, keys.Status: status.StatusKeyMiss})
			hdr, repeat := send(tc.to)
			requireTricksterResult(t, hdr, map[string]string{keys.Engine: offEngine, keys.Status: status.StatusHit})
			require.Equal(t, string(first), string(repeat))
			hdr, _ = send(tc.to.Add(offShift))
			requireTricksterResult(t, hdr, map[string]string{keys.Engine: offEngine, keys.Status: status.StatusKeyMiss})
			// the origin, asked the very same request, must give the same answer, less any members it changes every time
			resp, want := tricksterHarness{BaseAddr: tc.origin}.do(t, tc.path, tc.opts(tc.from, tc.to)...)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %.240s", want)
			requireSameJSON(t, want, first, tc.ignore)
		})
	}

	t.Run(offMySQLBackend, func(t *testing.T) {
		requireMySQLDeveloperEnvironment(t)
		direct, proxied := openIntegrationMySQL(t, offMySQLAddr), openIntegrationMySQL(t, h.MySQLAddr)
		requireNativeOff(t, h.MetricsAddr, offMySQLBackend, tripsTo, func(to time.Time) (any, any) {
			q := fmt.Sprintf(offMySQLSQL, tripsFrom.Unix(), to.Unix())
			return queryIntegrationMySQL(t, direct, q), queryIntegrationMySQL(t, proxied, q)
		})
	})

	t.Run(offPGBackend, func(t *testing.T) {
		pg := pgwireTargets()[0]
		direct, err := pgwireConnect(t, pg.OriginAddr, pg, pg.ClientPassword)
		if err != nil {
			t.Skipf("developer %s is unavailable at %s: %v", pg.Name, pg.OriginAddr, err)
		}
		proxied, err := pgwireConnect(t, pgAddr, pg, pg.ClientPassword)
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = direct.Close(context.Background())
			_ = proxied.Close(context.Background())
		})
		requireNativeOff(t, h.MetricsAddr, offPGBackend, tripsTo, func(to time.Time) (any, any) {
			q := fmt.Sprintf(pg.DeltaSQLs[0], tripsFrom.Format(time.RFC3339), to.Format(time.RFC3339))
			want, err := pgwireQuery(t, direct, q)
			require.NoError(t, err)
			got, err := pgwireQuery(t, proxied, q)
			require.NoError(t, err)
			return want, got
		})
	})

	t.Run("influx3 flight sql", func(t *testing.T) {
		direct := directFlightClient(t)
		proxied := readyFlightClient(t, flightAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		ctx = metadata.AppendToOutgoingContext(ctx, "database", offInfluxDB)
		requireNativeOff(t, h.MetricsAddr, offFlightBackend, i3To, func(to time.Time) (any, any) {
			q := fmt.Sprintf(offInflux3SQL, i3From.Format(time.RFC3339), to.Format(time.RFC3339))
			return collectFlightRows(t, ctx, direct, q), collectFlightRows(t, ctx, proxied, q)
		})
	})
}

func stepAlignmentOffHarness(t *testing.T) (tricksterHarness, string, string) {
	t.Helper()
	// sets off on every backend under test, and adds back the postgres and Flight SQL listeners the harness drops
	ports, release := portutil.Reserve(t, 2)
	pg := pgwireTargets()[0]
	origin := url.URL{
		Scheme: "postgres", Host: pg.OriginAddr, Path: "/" + pg.Database,
		User: url.UserPassword(pg.OriginUser, pg.OriginPassword),
	}
	h := configHarness(t, graphiteConfig(t.TempDir(), false), func(c *tkconfig.Config) {
		c.Listeners[offPGBackend] = &listener.Options{
			Protocol: listener.ProtocolPostgres, ListenAddress: offLocalhost, ListenPort: ports[0],
		}
		c.Listeners[offFlightName] = &listener.Options{
			Protocol: listener.ProtocolFlightSQL, ListenAddress: offLocalhost, ListenPort: ports[1],
		}
		backend := bo.New()
		backend.Provider, backend.OriginURL, backend.CacheName = providers.TimescaleDB, origin.String(), "mem1"
		backend.ListenerNames, backend.AuthenticatorName = []string{offPGBackend}, offPGAuth
		c.Backends[offPGBackend] = backend
		flight := c.Backends[offFlightBackend]
		flight.ListenerNames = append(flight.ListenerNames, offFlightName)
		c.Backends[graphiteBackend].Graphite.StaticRetentions = []gro.StaticRetention{
			{Pattern: offGraphiteFast, Retentions: offGraphiteRetentions},
		}
		for _, name := range offBackends {
			c.Backends[name].StepAlignment = timeseries.StepAlignmentOff
		}
	})
	releaseHarness := h.releasePorts
	h.releasePorts = func() {
		release()
		releaseHarness()
	}
	return h, net.JoinHostPort(offLocalhost, strconv.Itoa(ports[0])),
		net.JoinHostPort(offLocalhost, strconv.Itoa(ports[1]))
}

func readyFlightClient(t *testing.T, addr string) *flightsql.Client {
	t.Helper()
	// the Flight listener starts in the background, after the daemon is ready, and a client connects
	// only on its first call, so readiness is a listener that accepts
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
		}
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "flight sql listener never became ready")
	c, err := flightsql.NewClientCtx(context.Background(), addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func offRange(base time.Time, span time.Duration) (time.Time, time.Time) {
	// a closed range from the minute of base, both of whose ends sit off the step grids
	start := base.UTC().Truncate(time.Minute)
	return start.Add(offStartSkew), start.Add(span + offEndSkew)
}

func recentRange(latest time.Time) (time.Time, time.Time) {
	// the newest of Telegraf's data old enough to be fully written, as an offRange
	return offRange(latest.Add(-recentSpan-recentSettle), recentSpan)
}

func withBody(contentType, body string) requestOption {
	return func(o *requestOptions) {
		o.method, o.contentType, o.body = http.MethodPost, contentType, strings.NewReader(body)
	}
}

func requireSameJSON(t *testing.T, want, got []byte, ignore []string) {
	t.Helper()
	require.NotEmpty(t, want)
	if len(ignore) == 0 {
		require.Equal(t, string(want), string(got))
		return
	}
	var w, g map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(want, &w))
	require.NoError(t, json.Unmarshal(got, &g))
	for _, k := range ignore {
		delete(w, k)
		delete(g, k)
	}
	require.Equal(t, w, g)
}

func requireNativeOff(t *testing.T, metricsAddr, backend string, to time.Time,
	query func(to time.Time) (want, got any),
) {
	t.Helper()
	// a range, its repeat and its end shifted must each match the origin and come from the object tier
	// (two misses, one hit), never the delta tier
	misses := sqlCacheCount(t, metricsAddr, backend, offModeObject, status.StatusKeyMiss)
	hits := sqlCacheCount(t, metricsAddr, backend, offModeObject, status.StatusHit)
	delta := sqlCacheCount(t, metricsAddr, backend, offModeDelta, "")
	for _, end := range []time.Time{to, to, to.Add(offShift)} {
		want, got := query(end)
		require.Equal(t, want, got, "range ending %s", end)
	}
	require.Equal(t, misses+2, sqlCacheCount(t, metricsAddr, backend, offModeObject, status.StatusKeyMiss))
	require.Equal(t, hits+1, sqlCacheCount(t, metricsAddr, backend, offModeObject, status.StatusHit))
	require.Equal(t, delta, sqlCacheCount(t, metricsAddr, backend, offModeDelta, ""))
}

func sqlCacheCount(t *testing.T, metricsAddr, backend, mode, cacheStatus string) float64 {
	t.Helper()
	// an empty cacheStatus sums the mode over every status
	_, body := getBody(t, "http://"+metricsAddr+"/metrics")
	prefix := offSQLCacheTotal + `{backend_name="` + backend + `",cache_mode="` + mode + `",`
	var total float64
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, prefix) ||
			(cacheStatus != "" && !strings.Contains(line, `cache_status="`+cacheStatus+`"`)) {
			continue
		}
		value, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
		require.NoError(t, err)
		total += value
	}
	return total
}
