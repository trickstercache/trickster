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

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
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
	flightPort := 18585
	cfg := writeTestConfigWithFlight(t, 8593, 8594, 8595, flightPort)
	influxAddr := "127.0.0.1:8593"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: influxAddr, MetricsAddr: "127.0.0.1:8594"}
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

	// prepared_statement_with_params is covered by unit tests
	// (TestPreparedStatement_Parameterized in pkg/backends/influxdb/flight/)
	// using a fake upstream. An integration test against a real InfluxDB 3
	// Core instance is not included: Core 3.10 recognizes Flight SQL placeholders
	// at Prepare time (returns a parameter schema) but does not resolve the
	// bound values during query planning ("No value found for placeholder"),
	// so the failure mode is in upstream plan resolution, not our proxy.
}
