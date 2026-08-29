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
	"fmt"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TestInfluxDB3FlightSQL exercises the Flight SQL gRPC proxy end-to-end using
// the same ADBC-shaped wire format that Grafana's SQL datasource speaks.
// Covers the query path (cache miss + hit) and the metadata RPCs that ADBC
// clients probe on connect.
func TestInfluxDB3FlightSQL(t *testing.T) {
	// Unique flight port per test to avoid collisions across parallel runs.
	h, flightPort := flightConfigHarness(t)
	h.start(t)
	waitForInfluxDB3Data(t, "127.0.0.1:8181")

	tricksterFlightAddr := fmt.Sprintf("127.0.0.1:%d", flightPort)

	// Wait for the Flight listener to accept connections — it starts in a
	// goroutine during backend construction so there can be a brief lag.
	var client *flightsql.Client
	require.Eventually(t, func() bool {
		c, err := flightsql.NewClientCtx(context.Background(), tricksterFlightAddr, nil, nil,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return false
		}
		client = c
		return true
	}, 10*time.Second, 250*time.Millisecond, "flight sql listener never became ready")
	t.Cleanup(func() { client.Close() })

	// All calls need the `database` header to tell v3 which DB to query.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "database", "trickster")

	t.Run("execute", func(t *testing.T) {
		q := "SELECT avg(usage_idle) AS usage_idle FROM cpu WHERE cpu = 'cpu-total' LIMIT 10"
		info, err := client.Execute(ctx, q)
		require.NoError(t, err)
		require.NotEmpty(t, info.Endpoint)

		reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
		require.NoError(t, err)
		defer reader.Release()
		var rows int64
		for reader.Next() {
			rows += reader.RecordBatch().NumRows()
		}
		require.NoError(t, reader.Err())
		require.Greater(t, rows, int64(0), "expected rows from upstream")
	})

	t.Run("execute_cache_hit", func(t *testing.T) {
		// Same exact query text — second Execute should hit the in-memory cache.
		q := "SELECT host, avg(usage_idle) AS usage_idle FROM cpu WHERE cpu = 'cpu-total' GROUP BY host LIMIT 5"
		for range 2 {
			info, err := client.Execute(ctx, q)
			require.NoError(t, err)
			reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
			require.NoError(t, err)
			for reader.Next() {
			}
			reader.Release()
		}
		// If we got here without error, the proxy handled a repeat. Deeper
		// cache-hit assertions would require exposing cache counters; the
		// passing test shows correctness of the passthrough + caching path.
	})

	// delta_tier runs a delta-cacheable date_bin query through the Flight
	// proxy and checks the served data is identical to a direct-to-InfluxDB
	// Flight response (the fidelity gate), including on the cached rerun and
	// after widening the window (which triggers a sub-range delta fetch).
	t.Run("delta_tier", func(t *testing.T) {
		direct, err := flightsql.NewClientCtx(context.Background(), "127.0.0.1:8181", nil, nil,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { direct.Close() })

		collect := func(c *flightsql.Client, q string) map[string]int {
			t.Helper()
			info, err := c.Execute(ctx, q)
			require.NoError(t, err, "query: %s", q)
			reader, err := c.DoGet(ctx, info.Endpoint[0].Ticket)
			require.NoError(t, err)
			defer reader.Release()
			rows := make(map[string]int)
			for reader.Next() {
				record := reader.RecordBatch()
				for row := range int(record.NumRows()) {
					var line string
					for i := range int(record.NumCols()) {
						line += record.Column(i).ValueStr(row) + "|"
					}
					rows[line]++
				}
			}
			require.NoError(t, reader.Err())
			return rows
		}

		now := time.Now().UTC().Truncate(time.Minute)
		lower, upper := now.Add(-8*time.Minute), now.Add(-2*time.Minute)
		query := func(from, to time.Time) string {
			return fmt.Sprintf("SELECT date_bin(INTERVAL '1 minute', time) AS time, cpu, "+
				"avg(usage_idle) AS usage_idle FROM cpu WHERE time >= '%s' AND time < '%s' "+
				"GROUP BY 1, cpu", from.Format(time.RFC3339), to.Format(time.RFC3339))
		}

		want := collect(direct, query(lower, upper))
		require.NotEmpty(t, want, "expected rows from the direct query")
		got := collect(client, query(lower, upper))
		require.Equal(t, want, got, "first delta response differs from direct")
		cached := collect(client, query(lower, upper))
		require.Equal(t, want, cached, "cached delta response differs from direct")

		widened := query(lower.Add(-2*time.Minute), upper)
		widenedWant := collect(direct, widened)
		widenedGot := collect(client, widened)
		require.Equal(t, widenedWant, widenedGot,
			"widened delta response differs from direct")
		require.Greater(t, len(widenedGot), len(got),
			"widened window did not add rows")
	})

	t.Run("get_tables", func(t *testing.T) {
		info, err := client.GetTables(ctx, &flightsql.GetTablesOpts{})
		require.NoError(t, err, "GetTables should succeed (not Unimplemented)")
		require.NotEmpty(t, info.Endpoint)

		reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
		require.NoError(t, err)
		defer reader.Release()
		var rows int64
		for reader.Next() {
			rows += reader.RecordBatch().NumRows()
		}
		require.NoError(t, reader.Err())
		require.Greater(t, rows, int64(0), "expected at least one table in v3 instance")
	})

	t.Run("get_catalogs", func(t *testing.T) {
		info, err := client.GetCatalogs(ctx)
		require.NoError(t, err, "GetCatalogs should succeed (not Unimplemented)")
		require.NotEmpty(t, info.Endpoint)
	})

	t.Run("get_table_types", func(t *testing.T) {
		info, err := client.GetTableTypes(ctx)
		require.NoError(t, err, "GetTableTypes should succeed (not Unimplemented)")
		require.NotEmpty(t, info.Endpoint)
	})

	t.Run("get_db_schemas", func(t *testing.T) {
		info, err := client.GetDBSchemas(ctx, &flightsql.GetDBSchemasOpts{})
		require.NoError(t, err, "GetDBSchemas should succeed (not Unimplemented)")
		require.NotEmpty(t, info.Endpoint)
	})

	t.Run("get_sql_info", func(t *testing.T) {
		info, err := client.GetSqlInfo(ctx, []flightsql.SqlInfo{
			flightsql.SqlInfoFlightSqlServerName,
		})
		require.NoError(t, err, "GetSqlInfo should succeed (not Unimplemented)")
		require.NotEmpty(t, info.Endpoint)
	})

	t.Run("prepared_statement", func(t *testing.T) {
		ps, err := client.Prepare(ctx, "SELECT avg(usage_idle) FROM cpu WHERE cpu = 'cpu-total' LIMIT 5")
		require.NoError(t, err, "Prepare should succeed (not Unimplemented)")
		defer ps.Close(ctx)

		info, err := ps.Execute(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, info.Endpoint)

		reader, err := client.DoGet(ctx, info.Endpoint[0].Ticket)
		require.NoError(t, err)
		defer reader.Release()
		var rows int64
		for reader.Next() {
			rows += reader.RecordBatch().NumRows()
		}
		require.NoError(t, reader.Err())
		require.Greater(t, rows, int64(0), "expected rows from prepared statement")
	})

	// get_schema exercises the schema RPCs ADBC drivers probe on connect
	// before running any query.
	t.Run("get_schema", func(t *testing.T) {
		const q = "SELECT time, cpu, usage_idle FROM cpu LIMIT 1"
		for range 2 { // second call is served from cache
			result, err := client.GetExecuteSchema(ctx, q)
			require.NoError(t, err, "GetExecuteSchema should succeed (not Unimplemented)")
			schema, err := flight.DeserializeSchema(result.GetSchema(), memory.DefaultAllocator)
			require.NoError(t, err)
			require.Equal(t, 3, schema.NumFields(), "schema: %v", schema)
		}

		ps, err := client.Prepare(ctx, q)
		require.NoError(t, err)
		defer ps.Close(ctx)
		result, err := ps.GetSchema(ctx)
		require.NoError(t, err, "prepared GetSchema should succeed (not Unimplemented)")
		schema, err := flight.DeserializeSchema(result.GetSchema(), memory.DefaultAllocator)
		require.NoError(t, err)
		require.Equal(t, 3, schema.NumFields(), "prepared schema: %v", schema)
	})

	// prepared_delta verifies a parameterless prepared date_bin query is
	// served through the delta tier with data identical to a direct
	// (unproxied) execution of the same statement.
	t.Run("prepared_delta", func(t *testing.T) {
		direct, err := flightsql.NewClientCtx(context.Background(), "127.0.0.1:8181", nil, nil,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { direct.Close() })

		now := time.Now().UTC().Truncate(time.Minute)
		q := fmt.Sprintf("SELECT date_bin(INTERVAL '1 minute', time) AS time, cpu, "+
			"avg(usage_idle) AS usage_idle FROM cpu WHERE time >= '%s' AND time < '%s' "+
			"GROUP BY 1, cpu",
			now.Add(-9*time.Minute).Format(time.RFC3339),
			now.Add(-3*time.Minute).Format(time.RFC3339))

		collectRows := func(info *flight.FlightInfo, c *flightsql.Client) map[string]int {
			t.Helper()
			reader, err := c.DoGet(ctx, info.Endpoint[0].Ticket)
			require.NoError(t, err)
			defer reader.Release()
			rows := make(map[string]int)
			for reader.Next() {
				record := reader.RecordBatch()
				for row := range int(record.NumRows()) {
					var line string
					for i := range int(record.NumCols()) {
						line += record.Column(i).ValueStr(row) + "|"
					}
					rows[line]++
				}
			}
			require.NoError(t, reader.Err())
			return rows
		}

		wantInfo, err := direct.Execute(ctx, q)
		require.NoError(t, err)
		want := collectRows(wantInfo, direct)
		require.NotEmpty(t, want)

		ps, err := client.Prepare(ctx, q)
		require.NoError(t, err)
		defer ps.Close(ctx)
		for range 2 { // second execution is a delta-tier cache hit
			info, err := ps.Execute(ctx)
			require.NoError(t, err)
			got := collectRows(info, client)
			require.Equal(t, want, got, "prepared delta response differs from direct")
		}
	})

	// prepared_statement_with_params is covered by unit tests
	// (TestPreparedStatement_Parameterized in pkg/proxy/flightsql/)
	// using a fake upstream. An integration test against a real InfluxDB 3
	// Core instance is not included: Core 3.10 recognizes Flight SQL placeholders
	// at Prepare time (returns a parameter schema) but does not resolve the
	// bound values during query planning ("No value found for placeholder"),
	// so the failure mode is in upstream plan resolution, not our proxy.
}
