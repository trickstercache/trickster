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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

const (
	pgwireConformanceName     = "pgwire-conformance"
	pgwireConformanceTimeout  = 15 * time.Second
	pgwireSQLStateCanceled    = "57014"
	pgwireSQLStateBadPassword = "28P01"
	pgwireSecondUser          = "pgwire_conformance_second"
	pgwireTxIdle              = 'I'
	pgwireTxOpen              = 'T'
	pgwireTxFailed            = 'E'
)

type pgwireTarget struct {
	Name                 string
	Provider             string
	Dialect              string
	OriginAddr           string
	Database             string
	OriginUser           string
	OriginPassword       string
	ClientUser           string
	ClientPassword       string
	ScalarSQL            string
	LargeSQL             string
	LargeRows            int
	SlowSQL              string
	MissingSQL           string
	SetShowName          string
	SetShowSQL           string
	ShowSQL              string
	ObjectSQL            string
	DeltaSQLs            []string
	WeekSQL              string
	ZoneSQL              string
	ZoneChangesResults   bool
	SupportsCancel       bool
	SupportsTransactions bool
}

func pgwireTargets() []pgwireTarget {
	// The suite is the same for every engine the postgres listener can front;
	// only these facts differ, so serving another engine means adding an entry.
	return []pgwireTarget{{
		Name: "timescaledb", Provider: providers.TimescaleDB, Dialect: providers.Postgres, OriginAddr: "127.0.0.1:5432",
		Database: "trickster", OriginUser: "trickster", OriginPassword: "trickster-dev-upstream",
		ClientUser: "grafana_ro", ClientPassword: "trickster-dev-grafana",
		ScalarSQL: "SELECT 42::int4 AS i, 'text'::text AS t, 1.50::numeric AS n, NULL::text AS z, " +
			"TIMESTAMPTZ '2026-01-02 03:04:05+00' AS ts, 0.1::float8 AS f, true AS b",
		LargeSQL: "SELECT g, repeat('x', 100) FROM generate_series(1, 50000) AS g", LargeRows: 50000,
		SlowSQL: "SELECT pg_sleep(30)", MissingSQL: "SELECT * FROM __missing_pgwire_conformance_table",
		SetShowName: "TimeZone", SetShowSQL: "SET TIME ZONE 'America/New_York'", ShowSQL: "SHOW TimeZone",
		ObjectSQL: "SELECT cab_type, count(*) FROM trips GROUP BY 1 ORDER BY 1",
		// each statement takes the range's two RFC 3339 bounds
		DeltaSQLs: []string{
			"SELECT date_bin(INTERVAL '5 minutes', pickup_datetime, TIMESTAMPTZ '2000-01-01') AS time, " +
				"cab_type, count(*) AS trips, avg(total_amount) AS total FROM trips " +
				"WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1, 2 ORDER BY 1, 2",
			// the statement Grafana's $__timeGroup macro writes for TimescaleDB, with re-spelled constructs
			"SELECT time_bucket('300.000s',pickup_datetime) AS \"time\", count(*) AS trips, " +
				"max(extract(dow FROM pickup_datetime)) AS dow, max(cab_type::text) AS cab, max(passenger_count::int) AS riders " +
				"FROM trips WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1 DESC",
			// and the one it writes for plain PostgreSQL, whose bucket is a number of epoch seconds
			"SELECT floor(extract(epoch from pickup_datetime)/900)*900 AS \"time\", cab_type, count(*) FROM trips " +
				"WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1, 2 ORDER BY 1, 2",
			"SELECT time_bucket_gapfill('1h', pickup_datetime) AS time, count(*) FROM trips " +
				"WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1",
		},
		// week buckets sit on the engine's own grid, which is not the Unix epoch's
		WeekSQL: "SELECT time_bucket('7 days', pickup_datetime) AS time, count(*) FROM trips " +
			"WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1",
		ZoneSQL: "SET TIME ZONE 'Asia/Kolkata'", ZoneChangesResults: true,
		SupportsCancel: true, SupportsTransactions: true,
	}, {
		Name: "greptimedb", Provider: providers.GreptimeDB, Dialect: providers.GreptimeDB, OriginAddr: "127.0.0.1:4003",
		// GreptimeDB's read-only users cannot SET session variables. This fixture
		// account is used only for conformance; the Grafana backend remains read-only.
		Database: "public", OriginUser: "seeder", OriginPassword: "trickster-dev-seed",
		ClientUser: "seeder", ClientPassword: "trickster-dev-seed",
		ScalarSQL: "SELECT CAST(42 AS INT) AS i, 'text' AS t, CAST(1.50 AS DOUBLE) AS n, " +
			"CAST(NULL AS STRING) AS z, TIMESTAMP '2026-01-02 03:04:05' AS ts, true AS b",
		LargeSQL: "SELECT pickup_epoch, cab_type, passenger_count FROM trips " +
			"ORDER BY pickup_epoch, cab_type, passenger_count LIMIT 50000", LargeRows: 50000,
		MissingSQL:  "SELECT * FROM __missing_pgwire_conformance_table",
		SetShowName: "TimeZone", SetShowSQL: "SET time_zone = 'Asia/Kolkata'", ShowSQL: "SHOW TIMEZONE",
		ObjectSQL: "SELECT cab_type, count(*) FROM trips GROUP BY 1 ORDER BY 1",
		DeltaSQLs: []string{
			"SELECT date_bin(INTERVAL '5 minutes', pickup_datetime) AS time, cab_type, count(*) AS trips " +
				"FROM trips WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1, 2 ORDER BY 1, 2",
			"SELECT floor(extract(epoch FROM pickup_datetime)/900)*900 AS time, count(*) AS trips " +
				"FROM trips WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1",
			"SELECT date_bin('5m', pickup_datetime) AS time, count(*) AS trips " +
				"FROM trips WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1 DESC",
			"SELECT date_bin(INTERVAL '15 minutes', \"bucket\") AS time, sum(trips) AS trips " +
				"FROM trips_15m WHERE \"bucket\" >= '%s' AND \"bucket\" < '%s' GROUP BY 1 ORDER BY 1",
		},
		WeekSQL: "SELECT date_trunc('week', pickup_datetime) AS time, count(*) FROM trips " +
			"WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s' GROUP BY 1 ORDER BY 1",
		ZoneSQL: "SET LOCAL time_zone = 'Asia/Kolkata'",
		// The tested origin stubs transaction status and CancelRequest; neither
		// capability is usable.
		SupportsCancel: false, SupportsTransactions: false,
	}}
}

func pgwireRequireSQL(t *testing.T, scenario string, statements ...string) {
	t.Helper()
	for _, sql := range statements {
		if sql == "" {
			t.Skipf("the engine has no %s SQL configured", scenario)
		}
	}
}

func TestPGWireConformance(t *testing.T) {
	for _, target := range pgwireTargets() {
		t.Run(target.Name, func(t *testing.T) {
			probe, err := net.DialTimeout("tcp", target.OriginAddr, time.Second)
			if err != nil {
				t.Skipf("developer %s is unavailable at %s: %v", target.Name, target.OriginAddr, err)
			}
			_ = probe.Close()
			harness, proxyAddr := pgwireHarness(t, target)
			harness.start(t)
			runPGWireConformance(t, target, proxyAddr, harness.MetricsAddr)
		})
	}
}

func pgwireHarness(t *testing.T, target pgwireTarget) (tricksterHarness, string) {
	t.Helper()
	ports, release := portutil.Reserve(t, 1)
	origin := url.URL{
		Scheme: "postgres", Host: target.OriginAddr, Path: "/" + target.Database,
		User: url.UserPassword(target.OriginUser, target.OriginPassword),
	}
	harness := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[pgwireConformanceName] = &listener.Options{
			Protocol: listener.ProtocolPostgres, ListenAddress: "127.0.0.1", ListenPort: ports[0],
		}
		backend := bo.New()
		backend.Provider, backend.OriginURL = target.Provider, origin.String()
		backend.ListenerNames = []string{pgwireConformanceName}
		backend.CacheName = "mem1"
		backend.AuthenticatorName = pgwireConformanceName
		c.Authenticators[pgwireConformanceName] = &autho.Options{
			Provider: "basic", Users: configtypes.EnvStringMap{
				target.ClientUser: target.ClientPassword, pgwireSecondUser: target.ClientPassword,
			},
		}
		c.Backends[pgwireConformanceName] = backend
	})
	// the harness frees its reserved ports just before the daemon binds them
	releaseHarnessPorts := harness.releasePorts
	harness.releasePorts = func() {
		release()
		releaseHarnessPorts()
	}
	return harness, fmt.Sprintf("127.0.0.1:%d", ports[0])
}

func pgwireConnect(t *testing.T, address string, target pgwireTarget, password string) (*pgconn.PgConn, error) {
	t.Helper()
	dsn := url.URL{
		Scheme: "postgres", Host: address, Path: "/" + target.Database,
		User: url.UserPassword(target.ClientUser, password), RawQuery: "sslmode=disable",
	}
	ctx, cancel := context.WithTimeout(context.Background(), pgwireConformanceTimeout)
	defer cancel()
	return pgconn.Connect(ctx, dsn.String())
}

type pgwireResult struct {
	Columns []string
	Rows    [][]string
	Tag     string
}

func pgwireQuery(t *testing.T, conn *pgconn.PgConn, sql string) ([]pgwireResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pgwireConformanceTimeout)
	defer cancel()
	results, err := conn.Exec(ctx, sql).ReadAll()
	out := make([]pgwireResult, 0, len(results))
	for _, result := range results {
		converted := pgwireResult{Tag: result.CommandTag.String()}
		for _, field := range result.FieldDescriptions {
			converted.Columns = append(converted.Columns, fmt.Sprintf("%s:%d", field.Name, field.DataTypeOID))
		}
		for _, row := range result.Rows {
			values := make([]string, len(row))
			for i, value := range row {
				values[i] = "NULL"
				if value != nil {
					values[i] = string(value)
				}
			}
			converted.Rows = append(converted.Rows, values)
		}
		out = append(out, converted)
	}
	return out, err
}

func pgwireSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func runPGWireConformance(t *testing.T, target pgwireTarget, proxyAddr, metricsAddr string) {
	t.Helper()
	// every behavior a client sees through the listener is held to what it
	// sees from the origin itself
	direct, err := pgwireConnect(t, target.OriginAddr, target, target.ClientPassword)
	require.NoError(t, err)
	proxied, err := pgwireConnect(t, proxyAddr, target, target.ClientPassword)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = direct.Close(context.Background())
		_ = proxied.Close(context.Background())
	})
	both := func(sql string) (want, got []pgwireResult, wantErr, gotErr error) {
		want, wantErr = pgwireQuery(t, direct, sql)
		got, gotErr = pgwireQuery(t, proxied, sql)
		return
	}

	t.Run("startup reports the origin's parameters", func(t *testing.T) {
		for _, name := range []string{"server_version", "client_encoding", "DateStyle", "TimeZone", "integer_datetimes"} {
			require.Equal(t, direct.ParameterStatus(name), proxied.ParameterStatus(name), name)
		}
		require.NotZero(t, proxied.PID())
		require.NotEmpty(t, proxied.SecretKey())
	})

	t.Run("a wrong password is refused", func(t *testing.T) {
		_, err := pgwireConnect(t, proxyAddr, target, target.ClientPassword+"-wrong")
		require.Equal(t, pgwireSQLStateBadPassword, pgwireSQLState(err))
	})

	t.Run("scalar types render identically", func(t *testing.T) {
		want, got, wantErr, gotErr := both(target.ScalarSQL)
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Equal(t, want, got)
	})

	t.Run("an empty or comment-only query is answered", func(t *testing.T) {
		for _, sql := range []string{"", "-- ping", ";"} {
			want, got, wantErr, gotErr := both(sql)
			require.NoError(t, wantErr, sql)
			require.NoError(t, gotErr, sql)
			require.Equal(t, want, got, sql)
		}
	})

	t.Run("a multi-statement query returns every result", func(t *testing.T) {
		want, got, wantErr, gotErr := both("SELECT 1 AS a; SELECT 2 AS b, 3 AS c")
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Len(t, got, 2)
		require.Equal(t, want, got)
	})

	t.Run("the extended protocol binds parameters", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), pgwireConformanceTimeout)
		defer cancel()
		for _, conn := range []*pgconn.PgConn{direct, proxied} {
			result := conn.ExecParams(ctx, "SELECT $1::int4 + $2::int4", [][]byte{[]byte("40"), []byte("2")}, nil, nil, nil).Read()
			require.NoError(t, result.Err)
			require.Equal(t, "42", string(result.Rows[0][0]))
		}
	})

	t.Run("an error keeps its SQLSTATE and the session survives", func(t *testing.T) {
		pgwireRequireSQL(t, "error recovery", target.MissingSQL)
		_, _, wantErr, gotErr := both(target.MissingSQL)
		require.Error(t, wantErr)
		require.Equal(t, pgwireSQLState(wantErr), pgwireSQLState(gotErr))
		want, got, wantErr, gotErr := both("SELECT 1")
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Equal(t, want, got)
	})

	t.Run("transaction status follows the origin", func(t *testing.T) {
		if !target.SupportsTransactions {
			t.Skip("the engine has no transactions")
		}
		pgwireRequireSQL(t, "transaction error", target.MissingSQL)
		for _, step := range []struct {
			sql    string
			status byte
		}{
			{"BEGIN", pgwireTxOpen},
			{"SELECT 1", pgwireTxOpen},
			{target.MissingSQL, pgwireTxFailed},
			{"ROLLBACK", pgwireTxIdle},
		} {
			_, _ = pgwireQuery(t, direct, step.sql)
			_, _ = pgwireQuery(t, proxied, step.sql)
			require.Equal(t, step.status, direct.TxStatus(), step.sql)
			require.Equal(t, step.status, proxied.TxStatus(), step.sql)
		}
	})

	t.Run("session settings reach the origin and are reported back", func(t *testing.T) {
		pgwireRequireSQL(t, "session settings", target.SetShowSQL, target.ShowSQL)
		_, err := pgwireQuery(t, proxied, target.SetShowSQL)
		require.NoError(t, err)
		_, err = pgwireQuery(t, direct, target.SetShowSQL)
		require.NoError(t, err)
		want, got, wantErr, gotErr := both(target.ShowSQL)
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Equal(t, want, got)
		require.Equal(t, direct.ParameterStatus(target.SetShowName), proxied.ParameterStatus(target.SetShowName))
	})

	t.Run("a large result arrives whole", func(t *testing.T) {
		pgwireRequireSQL(t, "large result", target.LargeSQL)
		want, got, wantErr, gotErr := both(target.LargeSQL)
		require.NoError(t, wantErr)
		require.NoError(t, gotErr)
		require.Len(t, got[0].Rows, target.LargeRows)
		require.Equal(t, want, got)
	})

	t.Run("a cancel request stops the statement", func(t *testing.T) {
		if !target.SupportsCancel {
			t.Skip("the engine does not implement cancel requests")
		}
		pgwireRequireSQL(t, "cancellation", target.SlowSQL)
		failed := make(chan error, 1)
		go func() {
			_, err := pgwireQuery(t, proxied, target.SlowSQL)
			failed <- err
		}()
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), pgwireConformanceTimeout)
		defer cancel()
		require.NoError(t, proxied.CancelRequest(ctx))
		select {
		case err := <-failed:
			require.Equal(t, pgwireSQLStateCanceled, pgwireSQLState(err))
		case <-time.After(pgwireConformanceTimeout):
			t.Fatal("the statement was never canceled")
		}
		_, err := pgwireQuery(t, proxied, "SELECT 1")
		require.NoError(t, err, "the session must survive a cancel")
	})

	t.Run("cached results agree with the origin", func(t *testing.T) {
		pgwireRequireSQL(t, "object cache", target.ObjectSQL)
		if len(target.DeltaSQLs) == 0 {
			t.Skip("the engine has no delta-cache SQL configured")
		}
		// a fresh session: the one above changed its time zone, and with it its cache identity
		cached, err := pgwireConnect(t, proxyAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer cached.Close(context.Background())
		fresh, err := pgwireConnect(t, target.OriginAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer fresh.Close(context.Background())
		agree := func(sql string) {
			want, wantErr := pgwireQuery(t, fresh, sql)
			require.NoError(t, wantErr)
			require.NotEmpty(t, want[0].Rows, sql)
			for range 2 {
				got, gotErr := pgwireQuery(t, cached, sql)
				require.NoError(t, gotErr)
				require.Equal(t, want, got, sql)
			}
		}
		// old enough to be stable, so repeated requests are served from the cache
		start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
		agree(target.ObjectSQL)
		for _, statement := range target.DeltaSQLs {
			for _, hours := range [][2]time.Duration{{0, 3}, {1, 5}} {
				agree(fmt.Sprintf(statement, start.Add(hours[0]*time.Hour).Format(time.RFC3339),
					start.Add(hours[1]*time.Hour).Format(time.RFC3339)))
			}
		}
		_, body := getBody(t, "http://"+metricsAddr+"/metrics")
		for _, series := range []string{
			`cache_mode="object",cache_status="hit"`, `cache_mode="delta",cache_status="kmiss"`,
			`cache_mode="delta",cache_status="hit"`, `cache_mode="delta",cache_status="phit"`,
		} {
			require.Contains(t, body, `trickster_sql_query_cache_total{backend_name="`+pgwireConformanceName+
				`",`+series, series)
		}
		require.NotContains(t, body,
			`trickster_sql_query_rewrite_failures_total{backend_name="`+pgwireConformanceName+`"`)
	})

	t.Run("a cached range answers its sub-ranges and later ranges fetch only what is new", func(t *testing.T) {
		if len(target.DeltaSQLs) == 0 {
			t.Skip("the engine has no delta-cache SQL configured")
		}
		pgwireRequireSQL(t, "delta cache", target.DeltaSQLs[0])
		cached, err := pgwireConnect(t, proxyAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer cached.Close(context.Background())
		fresh, err := pgwireConnect(t, target.OriginAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer fresh.Close(context.Background())
		start := time.Now().UTC().Add(-96 * time.Hour).Truncate(time.Hour)
		statement := func(from, to time.Duration) string {
			return fmt.Sprintf(target.DeltaSQLs[0], start.Add(from*time.Hour).Format(time.RFC3339),
				start.Add(to*time.Hour).Format(time.RFC3339))
		}
		for _, step := range []struct {
			from, to time.Duration
			status   string
			// the statement's key is range-independent and already cached elsewhere on the axis
		}{{0, 6, "rmiss"}, {2, 4, "hit"}, {0, 6, "hit"}, {4, 9, "phit"}, {1, 8, "hit"}} {
			before := pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", step.status)
			want, err := pgwireQuery(t, fresh, statement(step.from, step.to))
			require.NoError(t, err)
			got, err := pgwireQuery(t, cached, statement(step.from, step.to))
			require.NoError(t, err)
			require.Equal(t, want, got, "hours %d to %d", step.from, step.to)
			require.Equal(t, before+1, pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", step.status),
				"hours %d to %d should be a %s", step.from, step.to, step.status)
		}
	})

	t.Run("week buckets land on the origin's grid", func(t *testing.T) {
		pgwireRequireSQL(t, "week buckets", target.WeekSQL)
		cached, err := pgwireConnect(t, proxyAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer cached.Close(context.Background())
		fresh, err := pgwireConnect(t, target.OriginAddr, target, target.ClientPassword)
		require.NoError(t, err)
		defer fresh.Close(context.Background())
		// Monday-aligned bounds, so no partial edge bucket is dropped from the cached answer
		upper := time.Now().UTC().Add(-7 * 24 * time.Hour).Truncate(24 * time.Hour)
		for upper.Weekday() != time.Monday {
			upper = upper.Add(-24 * time.Hour)
		}
		sql := fmt.Sprintf(target.WeekSQL, upper.Add(-21*24*time.Hour).Format(time.RFC3339), upper.Format(time.RFC3339))
		before := pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", "hit")
		want, err := pgwireQuery(t, fresh, sql)
		require.NoError(t, err)
		require.Len(t, want[0].Rows, 3)
		for range 2 {
			got, err := pgwireQuery(t, cached, sql)
			require.NoError(t, err)
			require.Equal(t, want, got)
		}
		// a bucket off the planned grid would have been refused and the statement relayed uncached
		require.Equal(t, before+1, pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", "hit"))
	})

	t.Run("sessions with different identities do not share answers", func(t *testing.T) {
		pgwireRequireSQL(t, "session time zone", target.ZoneSQL)
		if len(target.DeltaSQLs) < 2 {
			t.Skip("the engine has no session-aware delta-cache SQL configured")
		}
		pgwireRequireSQL(t, "session cache identity", target.DeltaSQLs[1])
		start := time.Now().UTC().Add(-120 * time.Hour).Truncate(time.Hour)
		// the second statement stays delta-cacheable under any session zone
		sql := fmt.Sprintf(target.DeltaSQLs[1], start.Format(time.RFC3339), start.Add(2*time.Hour).Format(time.RFC3339))
		session := func(address, user string, settings ...string) []pgwireResult {
			identity := target
			identity.ClientUser = user
			conn, err := pgwireConnect(t, address, identity, target.ClientPassword)
			require.NoError(t, err)
			defer conn.Close(context.Background())
			for _, setting := range settings {
				_, err = pgwireQuery(t, conn, setting)
				require.NoError(t, err)
			}
			out, err := pgwireQuery(t, conn, sql)
			require.NoError(t, err)
			return out
		}
		misses := func() float64 { return pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", "kmiss") }
		before := misses()
		utc := session(proxyAddr, target.ClientUser)
		require.Equal(t, session(target.OriginAddr, target.ClientUser), utc)
		// this identity already holds the statement over other ranges, so it is no key miss
		require.Equal(t, before, misses())
		// The session zone partitions the cache even for engines that render naive UTC timestamps.
		zoned := session(proxyAddr, target.ClientUser, target.ZoneSQL)
		require.Equal(t, session(target.OriginAddr, target.ClientUser, target.ZoneSQL), zoned)
		if target.ZoneChangesResults {
			require.NotEqual(t, utc, zoned)
		} else {
			require.Equal(t, utc, zoned)
		}
		require.Equal(t, before+1, misses())
		// and so does the client's user, which row-level security may depend on
		require.Equal(t, utc, session(proxyAddr, pgwireSecondUser))
		require.Equal(t, before+2, misses())
		// each identity is served from its own entry afterwards
		hits := pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", "hit")
		require.Equal(t, utc, session(proxyAddr, target.ClientUser))
		require.Equal(t, zoned, session(proxyAddr, target.ClientUser, target.ZoneSQL))
		require.Equal(t, hits+2, pgwireCacheCount(t, metricsAddr, target.Dialect, "delta", "hit"))
		_, body := getBody(t, "http://"+metricsAddr+"/metrics")
		require.NotContains(t, body,
			`trickster_sql_query_rewrite_failures_total{backend_name="`+pgwireConformanceName+`"`)
	})

	t.Run("relayed statements are classified", func(t *testing.T) {
		_, body := getBody(t, "http://"+metricsAddr+"/metrics")
		if target.ObjectSQL != "" || len(target.DeltaSQLs) != 0 {
			require.Contains(t, body, `trickster_sql_query_analysis_total{backend_name="`+pgwireConformanceName+`"`)
		}
		require.Contains(t, body, `trickster_proxy_requests_total{backend_name="`+pgwireConformanceName+`"`)
	})
}

func pgwireCacheCount(t *testing.T, metricsAddr, dialect, mode, status string) float64 {
	t.Helper()
	metrics := metricsutil.ScrapeURL(t, "http://"+metricsAddr+"/metrics", nil)
	return metrics[metricsutil.Key("trickster_sql_query_cache_total", map[string]string{
		"backend_name": pgwireConformanceName, "dialect": dialect, "cache_mode": mode, "cache_status": status,
	})]
}

func TestPGWireCacheCountSeparatesDialects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `trickster_sql_query_cache_total{backend_name="pgwire-conformance",cache_mode="delta",cache_status="hit",dialect="greptimedb"} 7`)
		_, _ = fmt.Fprintln(w, `trickster_sql_query_cache_total{dialect="postgres",cache_status="hit",cache_mode="delta",backend_name="pgwire-conformance"} 3`)
		_, _ = fmt.Fprintln(w, `trickster_sql_query_cache_total{backend_name="other",cache_mode="delta",cache_status="hit",dialect="postgres"} 9`)
		_, _ = fmt.Fprintln(w, `trickster_sql_query_cache_total{backend_name="pgwire-conformance",cache_mode="object",cache_status="hit",dialect="postgres"} 11`)
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	require.Equal(t, float64(7), pgwireCacheCount(t, address, providers.GreptimeDB, "delta", "hit"))
	require.Equal(t, float64(3), pgwireCacheCount(t, address, providers.Postgres, "delta", "hit"))
	require.Zero(t, pgwireCacheCount(t, address, providers.Postgres, "delta", "kmiss"))
}
