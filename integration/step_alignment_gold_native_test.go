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
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	goldPGSQL = "SELECT date_bin(INTERVAL '5 minutes', pickup_datetime, TIMESTAMPTZ '2000-01-01') AS time, " +
		"count(*) AS trips FROM trips WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1"
	goldPGTimeLayout      = "2006-01-02 15:04:05-07"
	goldPartialFetches    = "trickster_proxy_partial_bucket_fetches_total"
	goldNativeTripsStep   = 5 * time.Minute
	goldNativeInflux3Step = 10 * time.Second
)

var goldNativeProtocols = []struct{ backend, protocol string }{
	{offMySQLBackend, listener.ProtocolMySQL},
	{offPGBackend, listener.ProtocolPostgres},
	{offFlightBackend, listener.ProtocolFlightSQL},
}

func TestStepAlignmentGoldNative(t *testing.T) {
	// the gold test through the MySQL, PostgreSQL and Flight SQL listeners: each mode's answer, first
	// and repeat, is the origin's own over the range that mode serves, by label
	waitForInfluxDB3Data(t, offInfluxDB3Addr)
	h, addrs := goldNativeHarness(t)
	h.start(t)

	now := time.Now().UTC()
	tripsFrom, tripsTo := offRange(now.Add(-26*time.Hour), time.Hour)
	i3From, i3To := offRange(now.Add(-20*time.Minute), 10*time.Minute)

	t.Run(offMySQLBackend, func(t *testing.T) {
		requireMySQLDeveloperEnvironment(t)
		direct := openIntegrationMySQL(t, offMySQLAddr)
		query := func(db *sql.DB, from, to time.Time) map[int64]float64 {
			return mysqlGoldValues(t, db, fmt.Sprintf(offMySQLSQL, from.Unix(), to.Unix()))
		}
		for _, mode := range goldModes {
			t.Run(mode.String(), func(t *testing.T) {
				proxied := openIntegrationMySQL(t, addrs[goldBackend(offMySQLBackend, mode)])
				requireGoldNative(t, h.MetricsAddr, goldBackend(offMySQLBackend, mode), mode, goldNativeTripsStep,
					tripsFrom, tripsTo, func(from, to time.Time) map[int64]float64 { return query(direct, from, to) },
					func() map[int64]float64 { return query(proxied, tripsFrom, tripsTo) })
			})
		}
	})

	t.Run(offPGBackend, func(t *testing.T) {
		pg := pgwireTargets()[0]
		direct, err := pgwireConnect(t, pg.OriginAddr, pg, pg.ClientPassword)
		if err != nil {
			t.Skipf("developer %s is unavailable at %s: %v", pg.Name, pg.OriginAddr, err)
		}
		t.Cleanup(func() { _ = direct.Close(context.Background()) })
		for _, mode := range goldModes {
			t.Run(mode.String(), func(t *testing.T) {
				proxied, err := pgwireConnect(t, addrs[goldBackend(offPGBackend, mode)], pg, pg.ClientPassword)
				require.NoError(t, err)
				t.Cleanup(func() { _ = proxied.Close(context.Background()) })
				query := func(conn *pgconn.PgConn, from, to time.Time) map[int64]float64 {
					results, err := pgwireQuery(t, conn,
						fmt.Sprintf(goldPGSQL, from.Format(time.RFC3339), to.Format(time.RFC3339)))
					require.NoError(t, err)
					return pgwireGoldValues(t, results)
				}
				requireGoldNative(t, h.MetricsAddr, goldBackend(offPGBackend, mode), mode, goldNativeTripsStep,
					tripsFrom, tripsTo, func(from, to time.Time) map[int64]float64 { return query(direct, from, to) },
					func() map[int64]float64 { return query(proxied, tripsFrom, tripsTo) })
			})
		}
	})

	t.Run("influx3 flight sql", func(t *testing.T) {
		direct := directFlightClient(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		t.Cleanup(cancel)
		ctx = metadata.AppendToOutgoingContext(ctx, "database", offInfluxDB)
		query := func(c *flightsql.Client, from, to time.Time) map[int64]float64 {
			return flightGoldValues(t, ctx, c, fmt.Sprintf(offInflux3SQL, from.Format(time.RFC3339), to.Format(time.RFC3339)))
		}
		for _, mode := range goldModes {
			t.Run(mode.String(), func(t *testing.T) {
				var proxied *flightsql.Client
				// the Flight listener starts in the background, so it can lag the daemon's readiness
				require.Eventually(t, func() bool {
					c, err := flightsql.NewClientCtx(context.Background(), addrs[goldBackend(offFlightBackend, mode)],
						nil, nil, grpc.WithTransportCredentials(insecure.NewCredentials()))
					proxied = c
					return err == nil
				}, 10*time.Second, 250*time.Millisecond, "flight sql listener never became ready")
				t.Cleanup(func() { proxied.Close() })
				requireGoldNative(t, h.MetricsAddr, goldBackend(offFlightBackend, mode), mode, goldNativeInflux3Step,
					i3From, i3To, func(from, to time.Time) map[int64]float64 { return query(direct, from, to) },
					func() map[int64]float64 { return query(proxied, i3From, i3To) })
			})
		}
	})
}

func requireGoldNative(t *testing.T, metricsAddr, backend string, mode timeseries.StepAlignment,
	step time.Duration, from, to time.Time, origin func(from, to time.Time) map[int64]float64,
	proxied func() map[int64]float64,
) {
	t.Helper()
	servedFrom, servedTo := goldRange(mode, from, to, step)
	want := origin(servedFrom, servedTo)
	require.NotEmpty(t, want, "the origin has no data for %s", backend)
	start, end := from.Truncate(step), to.Truncate(step)
	_, partialEnd := mode.Edges()
	fetches := goldPartialFetchCount(t, metricsAddr, backend)
	for _, attempt := range []string{"first", "repeat"} {
		got := proxied()
		requireSameBuckets(t, want, got, attempt)
		// no mode shows a bucket past the client's range, and only a partial end shows the last
		_, sawEnd := got[end.Unix()]
		require.Equal(t, partialEnd == timeseries.EdgePartial && sawEnd, sawEnd, attempt)
		for label := range got {
			require.False(t, label < start.Unix() || label > end.Unix(), "%s: label %d", attempt, label)
		}
	}
	// partial buckets are fetched, from the object tier, under the partial modes only
	fetched := goldPartialFetchCount(t, metricsAddr, backend) - fetches
	if mode == timeseries.StepAlignmentTruncate || mode == timeseries.StepAlignmentDrop {
		require.Zero(t, fetched, "partial bucket fetches")
	} else {
		require.Positive(t, fetched, "partial bucket fetches")
	}
}

func goldPartialFetchCount(t *testing.T, metricsAddr, backend string) float64 {
	t.Helper()
	_, body := getBody(t, "http://"+metricsAddr+"/metrics")
	prefix := goldPartialFetches + `{backend_name="` + backend + `",`
	var total float64
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
		require.NoError(t, err)
		total += value
	}
	return total
}

func goldNativeHarness(t *testing.T) (tricksterHarness, map[string]string) {
	t.Helper()
	// a copy of each native backend per mode, each on a listener of its own
	ports, release := portutil.Reserve(t, len(goldNativeProtocols)*len(goldModes))
	addrs := make(map[string]string, len(ports))
	pg := pgwireTargets()[0]
	origin := url.URL{
		Scheme: "postgres", Host: pg.OriginAddr, Path: "/" + pg.Database,
		User: url.UserPassword(pg.OriginUser, pg.OriginPassword),
	}
	h := configHarness(t, func(c *tkconfig.Config) {
		i := 0
		for _, native := range goldNativeProtocols {
			for _, mode := range goldModes {
				name := goldBackend(native.backend, mode)
				c.Listeners[name] = &listener.Options{
					Protocol: native.protocol, ListenAddress: offLocalhost, ListenPort: ports[i],
				}
				addrs[name] = net.JoinHostPort(offLocalhost, strconv.Itoa(ports[i]))
				i++
				var o *bo.Options
				if native.backend == offPGBackend {
					// the harness drops the postgres backends, which need a listener it doesn't reserve
					o = bo.New()
					o.Provider, o.OriginURL, o.CacheName = providers.TimescaleDB, origin.String(), "mem1"
					o.AuthenticatorName = offPGAuth
				} else {
					o = c.Backends[native.backend].Clone()
				}
				o.Name, o.StepAlignment, o.ListenerNames = name, mode, []string{name}
				c.Backends[name] = o
			}
		}
	})
	releaseHarness := h.releasePorts
	h.releasePorts = func() {
		release()
		releaseHarness()
	}
	return h, addrs
}

func mysqlGoldValues(t *testing.T, db *sql.DB, query string) map[int64]float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, query)
	require.NoError(t, err, "query failed: %s", query)
	defer rows.Close()
	out := make(map[int64]float64)
	for rows.Next() {
		var label int64
		var value float64
		require.NoError(t, rows.Scan(&label, &value))
		out[label] = value
	}
	require.NoError(t, rows.Err())
	return out
}

func pgwireGoldValues(t *testing.T, results []pgwireResult) map[int64]float64 {
	t.Helper()
	out := make(map[int64]float64)
	for _, result := range results {
		for _, row := range result.Rows {
			label, err := time.Parse(goldPGTimeLayout, row[0])
			require.NoError(t, err)
			value, err := strconv.ParseFloat(row[1], 64)
			require.NoError(t, err)
			out[label.Unix()] = value
		}
	}
	return out
}

func flightGoldValues(t *testing.T, ctx context.Context, c *flightsql.Client, query string) map[int64]float64 {
	t.Helper()
	info, err := c.Execute(ctx, query)
	require.NoError(t, err, "query: %s", query)
	reader, err := c.DoGet(ctx, info.Endpoint[0].Ticket)
	require.NoError(t, err)
	defer reader.Release()
	out := make(map[int64]float64)
	for reader.Next() {
		record := reader.RecordBatch()
		labels, ok := record.Column(0).(*array.Timestamp)
		require.True(t, ok, "time column %T", record.Column(0))
		values, ok := record.Column(1).(*array.Float64)
		require.True(t, ok, "value column %T", record.Column(1))
		unit := labels.DataType().(*arrow.TimestampType).Unit
		for row := range int(record.NumRows()) {
			if values.IsNull(row) {
				continue
			}
			out[labels.Value(row).ToTime(unit).Unix()] = values.Value(row)
		}
	}
	require.NoError(t, reader.Err())
	return out
}
