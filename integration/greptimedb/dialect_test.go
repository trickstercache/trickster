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
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

type textQuery func(string) ([][]string, error)

func pgTextQuery(ctx context.Context, c *pgconn.PgConn) textQuery {
	return func(query string) ([][]string, error) {
		results, err := c.Exec(ctx, query).ReadAll()
		if err != nil {
			return nil, err
		}
		if len(results) != 1 {
			return nil, fmt.Errorf("expected one PostgreSQL result, got %d", len(results))
		}
		rows := make([][]string, len(results[0].Rows))
		for i, row := range results[0].Rows {
			rows[i] = make([]string, len(row))
			for j, v := range row {
				if v == nil {
					return nil, fmt.Errorf("unexpected NULL at row %d column %d", i, j)
				}
				rows[i][j] = string(v)
			}
		}
		return rows, nil
	}
}

func mysqlTextQuery(ctx context.Context, db *sql.DB) textQuery {
	return func(query string) ([][]string, error) {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		var result [][]string
		for rows.Next() {
			values := make([]sql.NullString, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			row := make([]string, len(values))
			for i, value := range values {
				if !value.Valid {
					return nil, fmt.Errorf("unexpected NULL at column %d", i)
				}
				row[i] = value.String
			}
			result = append(result, row)
		}
		return result, rows.Err()
	}
}

func floorBucket(epoch int64) int64 {
	const width = int64(300)
	q := epoch / width
	if epoch%width < 0 {
		q--
	}
	return q * width
}

func bucketOracle(rows [][]string) (map[int64]int64, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty raw timestamp fixture")
	}
	want := make(map[int64]int64)
	for _, row := range rows {
		if len(row) != 1 {
			return nil, fmt.Errorf("expected one raw timestamp column")
		}
		epoch, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return nil, err
		}
		want[floorBucket(epoch)]++
	}
	return want, nil
}

func checkBuckets(rows [][]string, want map[int64]int64) error {
	if len(rows) == 0 || len(want) == 0 {
		return fmt.Errorf("empty bucket comparison")
	}
	got := make(map[int64]int64, len(rows))
	var previous int64
	for i, row := range rows {
		if len(row) != 2 {
			return fmt.Errorf("expected bucket and count, got %v", row)
		}
		epoch, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return err
		}
		count, err := strconv.ParseInt(row[1], 10, 64)
		if err != nil {
			return err
		}
		if epoch%300 != 0 || count <= 0 || (i > 0 && epoch <= previous) {
			return fmt.Errorf("unaligned, unordered, duplicate or empty bucket: %v", row)
		}
		got[epoch], previous = count, epoch
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("bucket counts differ from independently grouped raw rows")
	}
	return nil
}

func checkBucketSQL(query textQuery, r *report, protocol string) error {
	forms := []struct {
		name, expression string
		mysqlSyntaxError bool
	}{
		{"extract", "CAST(floor(extract(epoch FROM pickup_datetime) / 300) * 300 AS BIGINT)", false},
		{"date_part", "CAST(floor(date_part('epoch', pickup_datetime) / 300) * 300 AS BIGINT)", false},
		{"date_bin_interval", "CAST(extract(epoch FROM date_bin(INTERVAL '5 minutes', pickup_datetime)) AS BIGINT)", true},
		{"date_bin_unit", "CAST(extract(epoch FROM date_bin(INTERVAL '5' MINUTE, pickup_datetime)) AS BIGINT)", false},
		{"date_bin_compact", "CAST(extract(epoch FROM date_bin('5m', pickup_datetime)) AS BIGINT)", false},
		{"interval_cast", "CAST(extract(epoch FROM date_bin('5 minutes'::interval, pickup_datetime)) AS BIGINT)", false},
	}
	var evidence []map[string]any
	var failures []error
	defer func() { r.Facts[protocol+"_bucket_cases"] = evidence }()
	for _, shift := range []time.Duration{0, 37 * time.Second} {
		from, to := r.From.Add(shift), r.To.Add(-shift)
		where := fmt.Sprintf(" WHERE pickup_datetime >= '%s' AND pickup_datetime < '%s'", from.Format(time.RFC3339), to.Format(time.RFC3339))
		raw, err := query("SELECT pickup_epoch FROM trips" + where)
		if err != nil {
			return err
		}
		want, err := bucketOracle(raw)
		if err != nil {
			return err
		}
		for _, form := range forms {
			sql := "SELECT " + form.expression + " AS bucket_epoch, count(*) AS n FROM trips" + where + " GROUP BY bucket_epoch ORDER BY bucket_epoch"
			rows, err := query(sql)
			item := map[string]any{"form": form.name, "sql": sql, "from": from, "to": to, "source_rows": len(raw), "buckets": rows}
			evidence = append(evidence, item)
			if form.mysqlSyntaxError && protocol == "mysql" {
				// MySQL requires INTERVAL expr unit; an arbitrary query failure is not evidence of rejection.
				var syntaxErr *mysql.MySQLError
				if errors.As(err, &syntaxErr) && syntaxErr.Number == 1149 && string(syntaxErr.SQLState[:]) == "42000" {
					item["expected_syntax_error"] = syntaxErr.Error()
					continue
				}
				err = fmt.Errorf("expected MySQL interval syntax error, got %v", err)
			}
			if err == nil {
				err = checkBuckets(rows, want)
			}
			if err != nil {
				item["error"] = err.Error()
				failures = append(failures, fmt.Errorf("%s, shift %s: %w", form.name, shift, err))
			}
		}
	}
	return errors.Join(failures...)
}

func checkQuoting(query textQuery, r *report, protocol string) error {
	var evidence []map[string]any
	defer func() { r.Facts[protocol+"_quoting_cases"] = evidence }()
	for _, tc := range []struct {
		sql, want string
		mysqlOnly bool
	}{
		{"SELECT 'literal' AS answer", "literal", false},
		{`SELECT "literal" AS answer`, "literal", true},
		{"SELECT 42 AS `answer`", "42", true},
	} {
		rows, err := query(tc.sql)
		item := map[string]any{"sql": tc.sql, "rows": rows}
		if err != nil {
			item["error"] = err.Error()
		}
		evidence = append(evidence, item)
		if tc.mysqlOnly && protocol == "pg" {
			if err == nil {
				return fmt.Errorf("PostgreSQL unexpectedly accepts MySQL quoting: %s", tc.sql)
			}
			continue
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(rows, [][]string{{tc.want}}) {
			return fmt.Errorf("%s: got %v, want %s", tc.sql, rows, tc.want)
		}
	}
	return nil
}

func TestBucketOracle(t *testing.T) {
	for _, tc := range []struct{ epoch, want int64 }{{-301, -600}, {-300, -300}, {-1, -300}, {0, 0}, {299, 0}, {300, 300}, {301, 300}} {
		t.Run(strconv.FormatInt(tc.epoch, 10), func(t *testing.T) {
			if got := floorBucket(tc.epoch); got != tc.want {
				t.Fatalf("bucket = %d, want %d", got, tc.want)
			}
		})
	}
	want, err := bucketOracle([][]string{{"0"}, {"0"}, {"299"}, {"300"}})
	if err != nil || !reflect.DeepEqual(want, map[int64]int64{0: 3, 300: 1}) {
		t.Fatalf("duplicates must count independently: %v, %v", want, err)
	}
	for _, rows := range [][][]string{nil, {{"1", "2"}}, {{"1.5"}}, {{"invalid"}}} {
		if _, err := bucketOracle(rows); err == nil {
			t.Fatalf("accepted invalid raw rows %v", rows)
		}
	}
}

func TestCheckBuckets(t *testing.T) {
	want := map[int64]int64{0: 3, 300: 1}
	for _, tc := range []struct {
		name string
		rows [][]string
		ok   bool
	}{
		{"valid", [][]string{{"0", "3"}, {"300", "1"}}, true},
		{"shifted_grid", [][]string{{"37", "3"}, {"337", "1"}}, false},
		{"wrong_count", [][]string{{"0", "2"}, {"300", "1"}}, false},
		{"missing_bucket", [][]string{{"0", "3"}}, false},
		{"reversed_order", [][]string{{"300", "1"}, {"0", "3"}}, false},
		{"duplicate_bucket", [][]string{{"0", "1"}, {"0", "2"}, {"300", "1"}}, false},
		{"empty", nil, false},
		{"malformed", [][]string{{"0"}}, false},
		{"non_integer", [][]string{{"0.5", "3"}, {"300", "1"}}, false},
		{"empty_count", [][]string{{"0", "0"}, {"300", "1"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBuckets(tc.rows, want)
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestCheckBucketSQL(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, failure string
		ok                      bool
	}{
		{"postgres", "pg", "", true},
		{"mysql", "mysql", "", true},
		{"mysql_missing_rejection", "mysql", "accept_interval", false},
		{"mysql_transport_error", "mysql", "transport", false},
		{"mysql_wrong_error", "mysql", "wrong_error", false},
		{"postgres_query_error", "pg", "query", false},
		{"wrong_counts", "pg", "counts", false},
		{"missing_raw_rows", "pg", "raw", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := report{From: time.Unix(0, 0), To: time.Unix(900, 0), Facts: map[string]any{}}
			query := func(sql string) ([][]string, error) {
				if strings.HasPrefix(sql, "SELECT pickup_epoch") {
					if tc.failure == "raw" {
						return nil, nil
					}
					return [][]string{{"100"}, {"400"}}, nil
				}
				if tc.failure == "query" {
					return nil, fmt.Errorf("query failed")
				}
				if tc.protocol == "mysql" && strings.Contains(sql, "INTERVAL '5 minutes'") {
					switch tc.failure {
					case "transport":
						return nil, fmt.Errorf("connection lost")
					case "wrong_error":
						return nil, &mysql.MySQLError{Number: 1045}
					case "accept_interval":
					default:
						return nil, &mysql.MySQLError{Number: 1149, SQLState: [5]byte{'4', '2', '0', '0', '0'}}
					}
				}
				if tc.failure == "counts" {
					return [][]string{{"0", "2"}, {"300", "1"}}, nil
				}
				return [][]string{{"0", "1"}, {"300", "1"}}, nil
			}
			if err := checkBucketSQL(query, &r, tc.protocol); (err == nil) != tc.ok {
				t.Fatalf("error = %v, want success %v", err, tc.ok)
			}
			if tc.failure != "raw" && len(r.Facts[tc.protocol+"_bucket_cases"].([]map[string]any)) != 12 {
				t.Fatal("must retain every SQL form in both windows, including after failures")
			}
		})
	}
}
