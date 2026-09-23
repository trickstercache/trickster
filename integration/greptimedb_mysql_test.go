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

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/pkg/backends/greptimedb"
	backend "github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
)

func TestGreptimeMySQLSharedBackend(t *testing.T) {
	if os.Getenv("TRICKSTER_GREPTIMEDB_MYSQL_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_MYSQL_ACCEPTANCE=1 with isolated developer GreptimeDB")
	}
	ports, release := portutil.Reserve(t, 2)
	mysqlOrigin := os.Getenv("GREPTIMEDB_MYSQL_ADDR")
	if mysqlOrigin == "" {
		mysqlOrigin = "127.0.0.1:4002"
	}
	pgOrigin := os.Getenv("GREPTIMEDB_PG_ADDR")
	if pgOrigin == "" {
		pgOrigin = "127.0.0.1:4003"
	}
	httpOrigin := os.Getenv("GREPTIMEDB_HTTP_URL")
	if httpOrigin == "" {
		httpOrigin = "http://127.0.0.1:4000"
	}
	harness := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners["greptime-mysql"] = &listener.Options{Protocol: listener.ProtocolMySQL, ListenAddress: "127.0.0.1", ListenPort: ports[0]}
		c.Listeners["greptime-pg"] = &listener.Options{Protocol: listener.ProtocolPostgres, ListenAddress: "127.0.0.1", ListenPort: ports[1]}
		o := bo.New()
		o.Provider, o.OriginURL = "greptimedb", httpOrigin
		o.ListenerNames = []string{"default", "greptime-mysql", "greptime-pg"}
		o.AuthenticatorName, o.CacheName = "greptimedb-grafana", "mem1"
		u := url.URL{Scheme: "mysql", Host: mysqlOrigin, User: url.UserPassword("grafana_ro", "trickster-dev-grafana"), Path: "/public"}
		o.MySQL = mo.New()
		o.MySQL.UpstreamURL = u.String()
		u.Scheme, u.Host = "postgres", pgOrigin
		o.Postgres = pgo.New()
		o.Postgres.UpstreamURL = u.String()
		c.Backends = bo.Lookup{"greptime": o}
	})
	originalRelease := harness.releasePorts
	harness.releasePorts = func() { release(); originalRelease() }
	harness.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	my, err := vtmysql.Connect(ctx, &vtmysql.ConnParams{Host: "127.0.0.1", Port: ports[0], Uname: "grafana_ro", Pass: "trickster-dev-grafana", DbName: "public"})
	require.NoError(t, err)
	defer my.Close()
	pg, err := pgwireConnect(t, fmt.Sprintf("127.0.0.1:%d", ports[1]), pgwireTarget{ClientUser: "grafana_ro", Database: "public"}, "trickster-dev-grafana")
	require.NoError(t, err)
	defer pg.Close(context.Background())
	for n := 0; n < 3; n++ {
		require.NoError(t, my.GetRawConn().SetDeadline(time.Now().Add(10*time.Second)))
		r, err := my.ExecuteFetch("SELECT 42 AS answer", 1, true)
		require.NoError(t, err)
		require.Len(t, r.Rows, 1)
		require.Equal(t, "42", r.Rows[0][0].ToString())
		rows, err := pgwireQuery(t, pg, "SELECT 42 AS answer")
		require.NoError(t, err)
		require.Equal(t, [][]string{{"42"}}, rows[0].Rows)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+harness.BaseAddr+"/greptime/v1/sql?sql="+url.QueryEscape("SELECT 42 AS answer"), nil)
		require.NoError(t, err)
		req.SetBasicAuth("grafana_ro", "trickster-dev-grafana")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var body struct {
			Code   int `json:"code"`
			Output []struct {
				Records struct {
					Rows [][]int64 `json:"rows"`
				} `json:"records"`
			} `json:"output"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Zero(t, body.Code)
		require.Len(t, body.Output, 1)
		require.Equal(t, [][]int64{{42}}, body.Output[0].Records.Rows)
	}
}

func TestGreptimeMySQLRealServer(t *testing.T) {
	if os.Getenv("TRICKSTER_GREPTIMEDB_MYSQL_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_MYSQL_ACCEPTANCE=1 with isolated developer GreptimeDB")
	}
	address := os.Getenv("GREPTIMEDB_MYSQL_ADDR")
	if address == "" {
		address = "127.0.0.1:4002"
	}
	probe, err := net.DialTimeout("tcp", address, time.Second)
	require.NoError(t, err, "requested GreptimeDB MySQL acceptance requires a running developer origin")
	_ = probe.Close()
	host, portText, err := net.SplitHostPort(address)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	params := vtmysql.ConnParams{Host: host, Port: port, Uname: "seeder", Pass: "trickster-dev-seed", DbName: "public"}
	connect := func(t *testing.T, params vtmysql.ConnParams) *vtmysql.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := vtmysql.Connect(ctx, &params)
		require.NoError(t, err)
		t.Cleanup(c.Close)
		return c
	}
	query := func(t *testing.T, c *vtmysql.Conn, sql string) *sqltypes.Result {
		t.Helper()
		require.NoError(t, c.GetRawConn().SetDeadline(time.Now().Add(15*time.Second)))
		r, err := c.ExecuteFetch(sql, 1000, true)
		require.NoError(t, err, sql)
		return r
	}
	writer := connect(t, params)
	table := fmt.Sprintf("trickster_mysql_%d", time.Now().UnixNano())
	query(t, writer, "CREATE TABLE "+table+" (ts TIMESTAMP(9) TIME INDEX, label STRING, reading BIGINT)")
	t.Cleanup(func() { query(t, writer, "DROP TABLE IF EXISTS "+table) })
	query(t, writer, "INSERT INTO "+table+" VALUES "+
		"('2026-01-01 00:00:00.123456789','A',9007199254740993),"+
		"('2026-01-01 00:00:01','a',2),('2026-01-01 00:00:02',NULL,3),"+
		"('2026-01-01 00:01:00','A',4),('2026-01-01 00:01:01','a',5),"+
		"('2026-01-01 00:02:00','A',6),('2026-01-01 00:02:01','a',7),"+
		"('2026-01-01 00:02:02',NULL,8)")
	params.Uname, params.Pass = "grafana_ro", "trickster-dev-grafana"
	cfg := cacheoptions.New()
	cfg.Name = table
	cfg.Provider = "memory"
	cache := cachemanager.NewCache(cachememory.New(cfg.Name, cfg), cachemanager.CacheOptions{}, cfg)
	require.NoError(t, cache.Connect())
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	server, err := backend.NewProtocolServer(backend.ProtocolConfig{
		BackendName: table, Upstream: params, Engine: greptimedb.MySQLEngine(), Cache: cache, CacheTTL: time.Minute,
		DownstreamUsers: map[string]string{"probe": "trickster-dev-probe"}, ConnectTimeout: 5 * time.Second, QueryTimeout: 10 * time.Second,
	})
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})
	newSession := func(t *testing.T) (*vtmysql.Conn, *vtmysql.Conn, func(string)) {
		t.Helper()
		direct := connect(t, params)
		proxy := connect(t, vtmysql.ConnParams{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Uname: "probe", Pass: "trickster-dev-probe", DbName: "public"})
		return direct, proxy, func(sql string) {
			t.Helper()
			want, got := query(t, direct, sql), query(t, proxy, sql)
			require.Equal(t, want.Fields, got.Fields)
			require.Equal(t, want.Rows, got.Rows)
			require.Equal(t, want.StatusFlags, got.StatusFlags)
		}
	}
	count := func(mode, status string) float64 {
		metric := &dto.Metric{}
		require.NoError(t, metrics.SQLQueryCache.WithLabelValues(table, "greptimedb", mode, status).Write(metric))
		return metric.GetCounter().GetValue()
	}
	t.Run("OPC retains exact timestamp integer and null", func(t *testing.T) {
		_, _, agree := newSession(t)
		sql := "SELECT ts,label,reading FROM " + table + " ORDER BY ts"
		agree(sql)
		before := count("object", "hit")
		agree(sql)
		require.Equal(t, before+1, count("object", "hit"))
	})
	t.Run("DPC miss hit and extended range", func(t *testing.T) {
		_, _, agree := newSession(t)
		for _, bucket := range []string{"DATE_BIN('1m',ts,FROM_UNIXTIME(0))", "DATE_TRUNC('minute',ts)"} {
			format := "SELECT " + bucket + " AS time,label,SUM(reading) AS total FROM " + table + " WHERE ts >= FROM_UNIXTIME(1767225600) AND ts < FROM_UNIXTIME(%d) GROUP BY time,label ORDER BY time,label"
			short := fmt.Sprintf(format, 1767225720)
			long := fmt.Sprintf(format, 1767225780)
			agree(short)
			before := count("delta", "hit")
			agree(short)
			require.Equal(t, before+1, count("delta", "hit"))
			before = count("delta", "phit")
			agree(long)
			require.Equal(t, before+1, count("delta", "phit"))
			agree(long)
		}
	})
	t.Run("error leaves connection usable", func(t *testing.T) {
		direct, proxy, agree := newSession(t)
		_, want := direct.ExecuteFetch("SHOW COUNT(*) WARNINGS", 100, true)
		_, got := proxy.ExecuteFetch("SHOW COUNT(*) WARNINGS", 100, true)
		require.Error(t, want)
		require.Equal(t, want.Error(), got.Error())
		agree("SELECT 17 AS answer")
	})
	t.Run("failed session setting conservatively bypasses cache", func(t *testing.T) {
		direct, proxy, agree := newSession(t)
		sql := "SELECT 23 AS answer"
		agree(sql)
		_, want := direct.ExecuteFetch("SET time_zone = '+08:00'", 100, true)
		_, got := proxy.ExecuteFetch("SET time_zone = '+08:00'", 100, true)
		require.Error(t, want)
		require.Equal(t, want.Error(), got.Error())
		before := count("object", "hit")
		agree("SHOW TIMEZONE")
		agree(sql)
		require.Equal(t, before, count("object", "hit"))
	})
	t.Run("partial buckets retain original query semantics", func(t *testing.T) {
		_, _, agree := newSession(t)
		for _, bounds := range []string{
			"ts >= FROM_UNIXTIME(1767225601) AND ts < FROM_UNIXTIME(1767225780)",
			"ts >= FROM_UNIXTIME(1767225600) AND ts <= FROM_UNIXTIME(1767225720)",
		} {
			sql := "SELECT DATE_BIN('1m',ts,FROM_UNIXTIME(0)) AS time,label,COUNT(*) AS total FROM " + table + " WHERE " + bounds + " GROUP BY time,label ORDER BY time,label"
			agree(sql)
			before := count("object", "hit")
			agree(sql)
			require.Equal(t, before+1, count("object", "hit"))
		}
	})
	t.Run("known transaction stubs still bypass caching", func(t *testing.T) {
		_, _, agree := newSession(t)
		sql := "SELECT 29 AS answer"
		agree(sql)
		before := count("object", "hit")
		agree("BEGIN")
		agree(sql)
		agree("ROLLBACK")
		require.Equal(t, before, count("object", "hit"))
		agree(sql)
		require.Equal(t, before+1, count("object", "hit"))
	})
	t.Run("unknown state disables caching", func(t *testing.T) {
		_, _, agree := newSession(t)
		agree("SET sql_mode = 'ANSI_QUOTES'")
		before := count("object", "hit")
		agree("SELECT 19 AS answer")
		agree("SELECT 19 AS answer")
		require.Equal(t, before, count("object", "hit"))
	})
}
