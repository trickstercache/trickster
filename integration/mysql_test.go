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

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mysqlbackend "github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
)

const (
	mysqlIntegrationUser     = "grafana_ro"
	mysqlIntegrationPassword = "trickster-dev-grafana"
	mysqlIntegrationDatabase = "trickster"
)

type mysqlQueryResult struct {
	columns []string
	types   []string
	rows    [][]string
}

func openIntegrationMySQL(t *testing.T, address string) *sql.DB {
	t.Helper()
	config := gomysql.NewConfig()
	config.User = mysqlIntegrationUser
	config.Passwd = mysqlIntegrationPassword
	config.Net = "tcp"
	config.Addr = address
	config.DBName = mysqlIntegrationDatabase
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 10 * time.Second
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "MySQL is not ready at %s", address)
	return db
}

func queryIntegrationMySQL(t *testing.T, db *sql.DB, query string) mysqlQueryResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, query)
	require.NoError(t, err, "query failed: %s", query)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	columnTypes, err := rows.ColumnTypes()
	require.NoError(t, err)
	result := mysqlQueryResult{columns: columns, types: make([]string, len(columnTypes))}
	for i, columnType := range columnTypes {
		result.types[i] = columnType.DatabaseTypeName()
	}
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		require.NoError(t, rows.Scan(destinations...))
		row := make([]string, len(values))
		for i, value := range values {
			if value == nil {
				row[i] = "NULL"
			} else {
				row[i] = hex.EncodeToString(value)
			}
		}
		result.rows = append(result.rows, row)
	}
	require.NoError(t, rows.Err())
	return result
}

func TestMySQLRealServerCompatibility(t *testing.T) {
	requireMySQLDeveloperEnvironment(t)
	harness := configHarness(t)
	harness.start(t)
	direct := openIntegrationMySQL(t, "127.0.0.1:3306")
	proxied := openIntegrationMySQL(t, harness.MySQLAddr)

	t.Run("database sql handshake ping and selection", func(t *testing.T) {
		for _, db := range []*sql.DB{direct, proxied} {
			var database string
			require.NoError(t, db.QueryRow("SELECT DATABASE()").Scan(&database))
			require.Equal(t, mysqlIntegrationDatabase, database)
		}
	})

	t.Run("supported result types agree", func(t *testing.T) {
		query := "SELECT CAST(-42 AS SIGNED) AS signed_value, " +
			"CAST(42 AS UNSIGNED) AS unsigned_value, CAST(3.50 AS DECIMAL(4,2)) AS decimal_value, " +
			"CAST('hello' AS CHAR) AS text_value, CAST(NULL AS CHAR) AS null_value, " +
			"TIMESTAMP('2026-01-02 03:04:05') AS datetime_value, X'00FF' AS binary_value"
		require.Equal(t, queryIntegrationMySQL(t, direct, query), queryIntegrationMySQL(t, proxied, query))
	})

	t.Run("OPC miss and hit agree", func(t *testing.T) {
		query := fmt.Sprintf("SELECT COUNT(*) AS value FROM trips WHERE trip_id >= %d", time.Now().UnixNano())
		want := queryIntegrationMySQL(t, direct, query)
		require.Equal(t, want, queryIntegrationMySQL(t, proxied, query))
		require.Equal(t, want, queryIntegrationMySQL(t, proxied, query))
	})

	t.Run("DPC miss and hit agree", func(t *testing.T) {
		var minimum time.Time
		require.NoError(t, direct.QueryRow("SELECT MIN(pickup_datetime) FROM trips").Scan(&minimum))
		lower := minimum.UTC().Truncate(5 * time.Minute)
		upper := lower.Add(30 * time.Minute)
		query := fmt.Sprintf("SELECT UNIX_TIMESTAMP(pickup_datetime) DIV 300 * 300 AS time, "+
			"COUNT(*) AS value FROM trips WHERE pickup_datetime >= FROM_UNIXTIME(%d) "+
			"AND pickup_datetime < FROM_UNIXTIME(%d) GROUP BY time ORDER BY time", lower.Unix(), upper.Unix())
		want := queryIntegrationMySQL(t, direct, query)
		require.Equal(t, want, queryIntegrationMySQL(t, proxied, query))
		require.Equal(t, want, queryIntegrationMySQL(t, proxied, query))
	})

	t.Run("errors and write policy agree", func(t *testing.T) {
		for _, query := range []string{
			"SELECT * FROM trickster.__missing_mysql_integration_table",
			"INSERT INTO trips (trip_id) VALUES (-1)",
		} {
			_, directErr := direct.Exec(query)
			_, proxyErr := proxied.Exec(query)
			require.Error(t, directErr)
			require.Error(t, proxyErr)
			var directMySQL, proxyMySQL *gomysql.MySQLError
			require.ErrorAs(t, directErr, &directMySQL)
			require.ErrorAs(t, proxyErr, &proxyMySQL)
			require.Equal(t, directMySQL.Number, proxyMySQL.Number)
		}
	})

	t.Run("no-backslash-escapes preserves executable comment policy", func(t *testing.T) {
		const modeSensitiveQuery = `SELECT 'x\' /*!80400 INTO @answer */`
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		directConn, err := direct.Conn(ctx)
		require.NoError(t, err)
		defer directConn.Close()
		_, err = directConn.ExecContext(ctx, "SET SESSION sql_mode = 'NO_BACKSLASH_ESCAPES'")
		require.NoError(t, err)
		_, err = directConn.ExecContext(ctx, modeSensitiveQuery)
		require.NoError(t, err)
		var directAnswer string
		require.NoError(t, directConn.QueryRowContext(ctx, "SELECT @answer").Scan(&directAnswer))
		require.Equal(t, `x\`, directAnswer)

		proxyConn, err := proxied.Conn(ctx)
		require.NoError(t, err)
		defer proxyConn.Close()
		_, err = proxyConn.ExecContext(ctx, "SET SESSION sql_mode = 'NO_BACKSLASH_ESCAPES'")
		require.NoError(t, err)
		_, err = proxyConn.ExecContext(ctx, modeSensitiveQuery)
		require.Error(t, err)
		var mysqlErr *gomysql.MySQLError
		require.ErrorAs(t, err, &mysqlErr)
		require.Equal(t, uint16(sqlerror.ERNotSupportedYet), mysqlErr.Number)

		var proxyAnswer sql.NullString
		require.NoError(t, proxyConn.QueryRowContext(ctx, "SELECT @answer").Scan(&proxyAnswer))
		require.False(t, proxyAnswer.Valid)
	})

	t.Run("reset and quit preserve synchronization", func(t *testing.T) {
		conn, err := proxied.Conn(context.Background())
		require.NoError(t, err)
		require.NoError(t, conn.Raw(func(raw any) error {
			resetter, ok := raw.(driver.SessionResetter)
			if !ok {
				return fmt.Errorf("driver connection %T has no SessionResetter", raw)
			}
			return resetter.ResetSession(context.Background())
		}))
		var answer int
		require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT 42").Scan(&answer))
		require.Equal(t, 42, answer)
		require.NoError(t, conn.Close())
	})

	t.Run("authentication failure", func(t *testing.T) {
		config := gomysql.NewConfig()
		config.User = mysqlIntegrationUser
		config.Passwd = "incorrect-password"
		config.Net = "tcp"
		config.Addr = harness.MySQLAddr
		config.DBName = mysqlIntegrationDatabase
		config.Timeout = 2 * time.Second
		db, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		defer db.Close()
		require.Error(t, db.Ping())
	})

	t.Run("Grafana built-in MySQL datasource", func(t *testing.T) {
		if connection, err := net.DialTimeout("tcp", "127.0.0.1:3000", time.Second); err != nil {
			t.Skipf("developer Grafana is unavailable: %v", err)
		} else {
			connection.Close()
		}
		_, port, err := net.SplitHostPort(harness.MySQLAddr)
		require.NoError(t, err)
		uid := fmt.Sprintf("mysql-integration-%d", time.Now().UnixNano())
		definition := map[string]any{
			"name": uid, "uid": uid, "type": "mysql", "access": "proxy",
			"url": "host.docker.internal:" + port, "user": mysqlIntegrationUser,
			"database": mysqlIntegrationDatabase,
			"jsonData": map[string]any{
				"database": mysqlIntegrationDatabase, "timezone": "UTC", "maxOpenConns": 2,
			},
			"secureJsonData": map[string]any{"password": mysqlIntegrationPassword},
		}
		status, body := grafanaMySQLRequest(t, http.MethodPost, "/api/datasources", definition)
		require.Equal(t, http.StatusOK, status, string(body))
		t.Cleanup(func() {
			status, body := grafanaMySQLRequest(t, http.MethodDelete, "/api/datasources/uid/"+uid, nil)
			require.Equal(t, http.StatusOK, status, string(body))
		})
		status, body = grafanaMySQLRequest(t, http.MethodGet, "/api/datasources/uid/"+uid+"/health", nil)
		require.Equal(t, http.StatusOK, status, string(body))
		require.Contains(t, string(body), "Database Connection OK")

		query := map[string]any{
			"from": "0", "to": fmt.Sprint(time.Now().UnixMilli()),
			"queries": []map[string]any{{
				"refId": "A", "datasource": map[string]string{"type": "mysql", "uid": uid},
				"rawSql": "SELECT 42 AS value", "format": "table",
			}},
		}
		status, body = grafanaMySQLRequest(t, http.MethodPost, "/api/ds/query", query)
		require.Equal(t, http.StatusOK, status, string(body))
		require.Contains(t, string(body), `"value"`)
	})

	t.Run("CLI client", func(t *testing.T) {
		if os.Getenv("TRICKSTER_MYSQL_CLI_TEST") == "" {
			t.Skip("set TRICKSTER_MYSQL_CLI_TEST=1 to run the Docker MySQL CLI matrix")
		}
		host, port, err := net.SplitHostPort(harness.MySQLAddr)
		require.NoError(t, err)
		_ = host
		command := exec.Command("docker", "compose", "-f",
			"../docs/developer/environment/docker-compose.yml", "exec", "-T",
			"-e", "MYSQL_PWD="+mysqlIntegrationPassword, "mysql",
			"mysql", "--protocol=TCP", "--host=host.docker.internal", "--port="+port,
			"--user="+mysqlIntegrationUser,
			"--database="+mysqlIntegrationDatabase, "--batch", "--skip-column-names", "--execute=SELECT 42")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "mysql CLI failed: %s", strings.TrimSpace(string(output)))
		require.Equal(t, []string{"42"}, slices.DeleteFunc(strings.Split(string(output), "\n"), func(s string) bool {
			return strings.TrimSpace(s) == ""
		}))
	})
}

type integrationMySQLRouteResolver map[string]backends.RouteDecision

func (r integrationMySQLRouteResolver) ResolveRoute(input backends.RouteInput) (backends.RouteDecision, bool) {
	decision, ok := r[input.Username]
	return decision, ok
}

type integrationRouteHealth int32

func (h integrationRouteHealth) Get() int32 { return int32(h) }

func TestMySQLRealServerUserRouter(t *testing.T) {
	requireMySQLDeveloperEnvironment(t)
	newTarget := func(name string) backends.Backend {
		t.Helper()
		o := bo.New()
		o.Name = name
		backend, err := backends.New(name, o, nil, nil, nil)
		require.NoError(t, err)
		return backend
	}
	targetA, targetB := newTarget("mysql-real-a"), newTarget("mysql-real-b")
	resolver := integrationMySQLRouteResolver{
		"alice": {Target: backends.RouteTarget{Backend: targetA}, Outcome: backends.RouteOutcomeSelected},
		"bob":   {Target: backends.RouteTarget{Backend: targetB}, Outcome: backends.RouteOutcomeSelected},
		"carol": {Target: backends.RouteTarget{Backend: targetA}, Outcome: backends.RouteOutcomeDefault},
		"down": {Target: backends.RouteTarget{Backend: targetB, Status: integrationRouteHealth(-1)},
			Outcome: backends.RouteOutcomeUnavailable},
	}
	upstream := func(database string) mysqlbackend.ProtocolConfig {
		return mysqlbackend.ProtocolConfig{
			BackendName: database, ConnectTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second,
			Upstream: vtmysql.ConnParams{
				Host: "127.0.0.1", Port: 3306, Uname: mysqlIntegrationUser,
				Pass: mysqlIntegrationPassword, DbName: database,
			},
		}
	}
	server, err := mysqlbackend.NewRoutedProtocolServer(mysqlbackend.ProtocolConfig{
		BackendName: "mysql-real-router", DownstreamUsers: map[string]string{
			"alice": "alice-password", "bob": "bob-password", "carol": "carol-password",
			"nobody": "nobody-password", "down": "down-password",
		},
	}, resolver, map[string]mysqlbackend.ProtocolConfig{
		"mysql-real-a": upstream("trickster"), "mysql-real-b": upstream("information_schema"),
	})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-done)
	})

	open := func(user, password string) *sql.DB {
		t.Helper()
		config := gomysql.NewConfig()
		config.User, config.Passwd = user, password
		config.Net, config.Addr = "tcp", listener.Addr().String()
		config.Timeout = 5 * time.Second
		db, openErr := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, openErr)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	for _, tc := range []struct{ user, password, database string }{
		{"alice", "alice-password", "trickster"},
		{"bob", "bob-password", "information_schema"},
		{"carol", "carol-password", "trickster"},
	} {
		t.Run(tc.user, func(t *testing.T) {
			db := open(tc.user, tc.password)
			var database string
			require.NoError(t, db.QueryRow("SELECT DATABASE()").Scan(&database))
			require.Equal(t, tc.database, database)
			var again string
			require.NoError(t, db.QueryRow("SELECT DATABASE()").Scan(&again))
			require.Equal(t, database, again, "route changed within one session")
		})
	}
	for _, user := range []string{"nobody", "down"} {
		t.Run(user, func(t *testing.T) {
			require.Error(t, open(user, user+"-password").Ping())
		})
	}
}

func requireMySQLDeveloperEnvironment(t *testing.T) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", "127.0.0.1:3306", time.Second)
	if err != nil {
		t.Skipf("developer MySQL is unavailable at 127.0.0.1:3306: %v", err)
	}
	connection.Close()
}

func grafanaMySQLRequest(t *testing.T, method, path string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, "http://127.0.0.1:3000"+path, body)
	require.NoError(t, err)
	request.SetBasicAuth("admin", "admin")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, data
}
