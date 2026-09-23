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

package greptimedb_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

const pgTypeQuery = `SELECT CAST('2026-09-20 03:04:05.123456789' AS TIMESTAMP(0)) AS ts0,
CAST('2026-09-20 03:04:05.123456789' AS TIMESTAMP(3)) AS ts3,
CAST('2026-09-20 03:04:05.123456789' AS TIMESTAMP(6)) AS ts6,
CAST('2026-09-20 03:04:05.123456789' AS TIMESTAMP(9)) AS ts9,
CAST('2026-09-20' AS DATE) AS d, CAST(1 AS SMALLINT) AS i2, CAST(2 AS INT) AS i4,
CAST(9007199254740993 AS BIGINT) AS i8, CAST(1.25 AS FLOAT) AS f4, CAST(2.5 AS DOUBLE) AS f8`

func emptyResult(result *pgconn.Result) bool {
	return result != nil && result.Err == nil && len(result.Rows) == 0 &&
		len(result.FieldDescriptions) == 0 && result.CommandTag.String() == ""
}

func protocolChecks(run func(string, func() error), r *report) {
	user := envOr("GREPTIMEDB_USER", "grafana_ro")
	password := envOr("GREPTIMEDB_PASSWORD", "trickster-dev-grafana")
	database := envOr("GREPTIMEDB_DATABASE", "public")
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(user, password),
		Host: envOr("GREPTIMEDB_PG_ADDR", "127.0.0.1:4003"), Path: "/" + database,
		RawQuery: "sslmode=disable&connect_timeout=10",
	}
	withPG := func(fn func(context.Context, *pgconn.PgConn) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgconn.Connect(ctx, u.String())
		if err != nil {
			return err
		}
		defer c.Close(ctx)
		return fn(ctx, c)
	}
	withMySQL := func(fn func(context.Context, *sql.DB) error) error {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.DBName = user, password, "tcp", database
		cfg.Addr = envOr("GREPTIMEDB_MYSQL_ADDR", "127.0.0.1:4002")
		cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 10*time.Second, 30*time.Second, 15*time.Second
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			return err
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return fn(ctx, db)
	}
	run("pg_startup_and_empty_queries", func() error {
		return withPG(func(ctx context.Context, c *pgconn.PgConn) error {
			status := map[string]string{}
			for _, key := range []string{"server_version", "server_encoding", "client_encoding", "DateStyle", "integer_datetimes", "TimeZone"} {
				status[key] = c.ParameterStatus(key)
			}
			r.Facts["pgconn_parameter_status_not_grafana_capture"] = status
			for _, query := range []string{"", "-- ping", "/* comment */", ";", "-- comment\n;"} {
				results, err := c.Exec(ctx, query).ReadAll()
				if err != nil {
					return fmt.Errorf("simple empty query %q: %w", query, err)
				}
				// pgconn exposes EmptyQueryResponse as one empty Result.
				if len(results) != 1 || !emptyResult(results[0]) || c.TxStatus() != 'I' {
					return fmt.Errorf("simple empty query %q: unexpected results/status", query)
				}
			}
			return c.Ping(ctx)
		})
	})
	run("pg_extended_query", func() error {
		return withPG(func(ctx context.Context, c *pgconn.PgConn) error {
			result := c.ExecParams(ctx, "SELECT $1::BIGINT AS answer", [][]byte{[]byte("42")}, []uint32{20}, []int16{0}, []int16{0}).Read()
			if result.Err != nil {
				return result.Err
			}
			if len(result.Rows) != 1 || len(result.Rows[0]) != 1 || string(result.Rows[0][0]) != "42" ||
				len(result.FieldDescriptions) != 1 || result.FieldDescriptions[0].DataTypeOID != 20 || c.TxStatus() != 'I' {
				return fmt.Errorf("unexpected extended query result")
			}
			result = c.ExecParams(ctx, "-- ping", nil, nil, nil, nil).Read()
			if !emptyResult(result) || c.TxStatus() != 'I' {
				return fmt.Errorf("extended comment-only query: %v", result.Err)
			}
			return nil
		})
	})
	run("pg_bucket_sql", func() error {
		return withPG(func(ctx context.Context, c *pgconn.PgConn) error {
			return checkBucketSQL(pgTextQuery(ctx, c), r, "pg")
		})
	})
	run("pg_quoting", func() error {
		return withPG(func(ctx context.Context, c *pgconn.PgConn) error {
			return checkQuoting(pgTextQuery(ctx, c), r, "pg")
		})
	})
	run("pg_cancel_capability", func() error {
		return withPG(func(_ context.Context, c *pgconn.PgConn) error {
			zeroKey := len(c.SecretKey()) == 4
			for _, b := range c.SecretKey() {
				zeroKey = zeroKey && b == 0
			}
			r.Facts["pg_cancel_capability"] = map[string]any{
				"pid_zero": c.PID() == 0, "key_zero": zeroKey,
				"supports_cancel": false, "basis": "upstream uses zero cancellation credentials and the default no-op cancel handler",
			}
			if c.PID() != 0 || !zeroKey {
				return fmt.Errorf("upstream cancellation credentials changed; re-evaluate support before setting conformance flags")
			}
			return nil
		})
	})
	run("pg_transaction_compatibility", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cfg, err := pgconn.ParseConfig(u.String())
		if err != nil {
			return err
		}
		var notices []string
		cfg.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) { notices = append(notices, notice.Message) }
		c, err := pgconn.ConnectConfig(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close(ctx)
		var trace []map[string]any
		defer func() {
			r.Facts["pg_transaction_compatibility"] = map[string]any{"steps": trace, "notices": notices, "supports_transactions": false}
		}()
		for _, step := range []struct {
			sql, value string
			status     byte
			wantErr    bool
		}{
			{"BEGIN", "", 'T', false},
			{"SELECT 1", "1", 'T', false},
			{"SELECT trickster_acceptance_missing_column FROM trips", "", 'E', true},
			{"ROLLBACK", "", 'I', false},
			{"SELECT 42", "42", 'I', false},
		} {
			results, err := c.Exec(ctx, step.sql).ReadAll()
			item := map[string]any{"sql": step.sql, "status": string(c.TxStatus())}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				item["sqlstate"] = pgErr.Code
			}
			trace = append(trace, item)
			if (err != nil) != step.wantErr || (step.wantErr && pgErr == nil) {
				return fmt.Errorf("%s: unexpected error %v", step.sql, err)
			}
			if c.TxStatus() != step.status {
				return fmt.Errorf("%s: status %c, want %c", step.sql, c.TxStatus(), step.status)
			}
			if step.value != "" {
				if len(results) != 1 || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 1 || string(results[0].Rows[0][0]) != step.value {
					return fmt.Errorf("%s: unexpected recovery query result", step.sql)
				}
				item["value"] = step.value
			}
		}
		for _, notice := range notices {
			if strings.Contains(notice, "transaction is not supported") && c.TxStatus() == 'I' {
				return nil
			}
		}
		return fmt.Errorf("missing upstream no-transactions warning or rollback did not restore idle status")
	})
	run("pg_type_oids_and_text", func() error {
		return withPG(func(ctx context.Context, c *pgconn.PgConn) error {
			results, err := c.Exec(ctx, pgTypeQuery).ReadAll()
			if err != nil {
				return err
			}
			wantOID := []uint32{1114, 1114, 1114, 1114, 1082, 21, 23, 20, 700, 701}
			// Pin the observed upstream wire rendering, not Arrow's stored precision.
			// The PG encoder renders six fractional digits even for TIMESTAMP(9).
			wantText := []string{"2026-09-20 03:04:05.000000", "2026-09-20 03:04:05.123000", "2026-09-20 03:04:05.123456", "2026-09-20 03:04:05.123456", "2026-09-20", "1", "2", "9007199254740993", "1.25", "2.5"}
			if len(results) != 1 || len(results[0].Rows) != 1 || len(results[0].FieldDescriptions) != len(wantOID) || len(results[0].Rows[0]) != len(wantOID) {
				return fmt.Errorf("unexpected typed query result shape")
			}
			var observed []map[string]any
			for i, f := range results[0].FieldDescriptions {
				observed = append(observed, map[string]any{"name": f.Name, "oid": f.DataTypeOID, "format": f.Format, "text": string(results[0].Rows[0][i])})
			}
			r.Facts["pgconn_type_metadata_not_grafana_capture"] = observed
			for i, f := range results[0].FieldDescriptions {
				if f.DataTypeOID != wantOID[i] || f.Format != 0 || string(results[0].Rows[0][i]) != wantText[i] {
					return fmt.Errorf("field %s: oid=%d format=%d text=%q, want oid=%d text=%q", f.Name, f.DataTypeOID, f.Format, results[0].Rows[0][i], wantOID[i], wantText[i])
				}
			}
			return nil
		})
	})
	run("http_sql", func() error {
		form := url.Values{"db": {database}, "sql": {"SELECT CAST(9007199254740993 AS BIGINT) AS answer"}}
		endpoint := strings.TrimRight(envOr("GREPTIMEDB_HTTP_URL", "http://127.0.0.1:4000"), "/") + "/v1/sql"
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.SetBasicAuth(user, password)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var body struct {
			Code   int    `json:"code"`
			Error  string `json:"error"`
			Output []struct {
				Records struct {
					Rows [][]json.Number `json:"rows"`
				} `json:"records"`
			} `json:"output"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
			return err
		}
		if resp.StatusCode != 200 || body.Code != 0 || body.Error != "" || len(body.Output) != 1 ||
			len(body.Output[0].Records.Rows) != 1 || len(body.Output[0].Records.Rows[0]) != 1 ||
			body.Output[0].Records.Rows[0][0] != json.Number("9007199254740993") {
			return fmt.Errorf("unexpected HTTP SQL response (HTTP %d): %+v", resp.StatusCode, body)
		}
		return nil
	})
	run("mysql_ping_and_query", func() error {
		return withMySQL(func(ctx context.Context, db *sql.DB) error {
			if err := db.PingContext(ctx); err != nil {
				return err
			}
			var version string
			if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
				return err
			}
			r.Facts["mysql_version"] = version
			var answer int64
			if err := db.QueryRowContext(ctx, "SELECT CAST(9007199254740993 AS BIGINT)").Scan(&answer); err != nil {
				return err
			}
			if answer != 9007199254740993 {
				return fmt.Errorf("unexpected MySQL value %d", answer)
			}
			return nil
		})
	})
	run("mysql_bucket_sql", func() error {
		return withMySQL(func(ctx context.Context, db *sql.DB) error {
			return checkBucketSQL(mysqlTextQuery(ctx, db), r, "mysql")
		})
	})
	run("mysql_quoting", func() error {
		return withMySQL(func(ctx context.Context, db *sql.DB) error {
			return checkQuoting(mysqlTextQuery(ctx, db), r, "mysql")
		})
	})
}

func TestEmptyResult(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result *pgconn.Result
		want   bool
	}{
		{"empty_query_response", &pgconn.Result{}, true},
		{"missing_response", nil, false},
		{"query_error", &pgconn.Result{Err: fmt.Errorf("query failed")}, false},
		{"row", &pgconn.Result{Rows: [][][]byte{{[]byte("1")}}}, false},
		{"schema", &pgconn.Result{FieldDescriptions: []pgconn.FieldDescription{{Name: "a"}}}, false},
		{"command_complete", &pgconn.Result{CommandTag: pgconn.NewCommandTag("SELECT 0")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := emptyResult(tt.result); got != tt.want {
				t.Fatalf("emptyResult = %v, want %v", got, tt.want)
			}
		})
	}
}
