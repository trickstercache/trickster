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

package mysql

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtenv"
)

func benchmarkResult(rowCount int) *sqltypes.Result {
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "number", Type: querypb.Type_INT64},
		{Name: "label", Type: querypb.Type_VARCHAR},
		{Name: "optional", Type: querypb.Type_VARCHAR},
		{Name: "observed_at", Type: querypb.Type_DATETIME},
		{Name: "payload", Type: querypb.Type_VARBINARY},
	}
	rows := make([][]sqltypes.Value, rowCount)
	for i := range rowCount {
		optional := sqltypes.NULL
		if i%2 == 0 {
			optional = sqltypes.NewVarChar("present")
		}
		rows[i] = []sqltypes.Value{
			sqltypes.NewInt64(int64(i * 60)),
			sqltypes.NewInt64(int64(i)),
			sqltypes.NewVarChar(fmt.Sprintf("series-%04d", i%100)),
			optional,
			sqltypes.NewDatetime("2026-08-01 00:00:00"),
			sqltypes.NewVarBinary("\x00\x01\x02benchmark-payload"),
		}
	}
	return &sqltypes.Result{Fields: fields, Rows: rows}
}

func benchmarkPlan() *sqlanalyzer.QueryPlan {
	return &sqlanalyzer.QueryPlan{
		OutputColumn: "time", OutputUnit: timeseries.DateTimeUnixSecs,
		Step: time.Minute, ValueColumns: []string{"number"},
	}
}

// benchmarkGroupedPlan exercises the collation-aware group comparator, which
// benchmarkPlan skips entirely by declaring no group columns.
func benchmarkGroupedPlan() *sqlanalyzer.QueryPlan {
	plan := benchmarkPlan()
	plan.GroupColumns = []string{"label"}
	return plan
}

// benchmarkGroupedResult repeats each timestamp across several label values.
// benchmarkResult gives every row a unique timestamp, which short-circuits the
// group comparison before it ever runs.
func benchmarkGroupedResult(rowCount int) *sqltypes.Result {
	const groupsPerBucket = 8
	result := benchmarkResult(rowCount)
	rows := make([][]sqltypes.Value, len(result.Rows))
	for i, row := range result.Rows {
		clone := slices.Clone(row)
		clone[0] = sqltypes.NewInt64(int64(i/groupsPerBucket) * 60)
		clone[2] = sqltypes.NewVarChar(fmt.Sprintf("series-%04d", i%groupsPerBucket))
		rows[i] = clone
	}
	return &sqltypes.Result{Fields: result.Fields, Rows: rows}
}

func BenchmarkMySQLResultHandling(b *testing.B) {
	for _, rowCount := range []int{10, 100, 1000, 10000} {
		result := benchmarkResult(rowCount)
		plan := benchmarkPlan()
		cached := &cachedQueryResult{
			result: result,
			extents: timeseries.ExtentList{{
				Start: time.Unix(0, 0), End: time.Unix(int64((rowCount-1)*60), 0),
			}},
		}
		name := fmt.Sprintf("rows_%d", rowCount)
		b.Run(name+"/ResultCodec", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				protoResult := sqltypes.ResultToProto3(result)
				encoded, err := protoResult.MarshalVT()
				if err != nil {
					b.Fatal(err)
				}
				decoded := &querypb.QueryResult{}
				if err := decoded.UnmarshalVT(encoded); err != nil {
					b.Fatal(err)
				}
				if sqltypes.Proto3ToResult(decoded) == nil {
					b.Fatal("decoded nil result")
				}
			}
		})
		b.Run(name+"/CacheEnvelope", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				encoded, err := marshalCachedQueryResult(cached)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := unmarshalCachedQueryResult(encoded); err != nil {
					b.Fatal(err)
				}
			}
		})
		parts := []*sqltypes.Result{
			{Fields: result.Fields, Rows: result.Rows[:rowCount/2]},
			{Fields: result.Fields, Rows: result.Rows[rowCount/2:]},
		}
		b.Run(name+"/Merge", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := dpcTestHandler.mergeResults(parts, plan); err != nil {
					b.Fatal(err)
				}
			}
		})
		reversed := &sqltypes.Result{Fields: result.Fields, Rows: slices.Clone(result.Rows)}
		slices.Reverse(reversed.Rows)
		extent := timeseries.Extent{
			Start: time.Unix(int64(rowCount/4*60), 0),
			End:   time.Unix(int64((rowCount-rowCount/4-1)*60), 0),
		}
		b.Run(name+"/CropSort", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := dpcTestHandler.cropAndSortResult(reversed, plan, extent); err != nil {
					b.Fatal(err)
				}
			}
		})
		groupedPlan := benchmarkGroupedPlan()
		grouped := benchmarkGroupedResult(rowCount)
		groupedParts := []*sqltypes.Result{
			{Fields: grouped.Fields, Rows: grouped.Rows[:rowCount/2]},
			{Fields: grouped.Fields, Rows: grouped.Rows[rowCount/2:]},
		}
		groupedReversed := &sqltypes.Result{Fields: grouped.Fields, Rows: slices.Clone(grouped.Rows)}
		slices.Reverse(groupedReversed.Rows)
		groupedExtent := timeseries.Extent{
			Start: time.Unix(0, 0),
			End:   time.Unix(int64(rowCount/8*60), 0),
		}
		b.Run(name+"/GroupedMerge", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := dpcTestHandler.mergeResults(groupedParts, groupedPlan); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/GroupedCropSort", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := dpcTestHandler.cropAndSortResult(groupedReversed, groupedPlan,
					groupedExtent); err != nil {
					b.Fatal(err)
				}
			}
		})
		h := &protocolHandler{config: ProtocolConfig{RetentionPoints: max(1, rowCount/2)}}
		b.Run(name+"/Retention", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				retained, _, err := h.applyRetentionSorted(result, cached.extents, plan, 0)
				if err != nil {
					b.Fatal(err)
				}
				if len(retained.Rows) == 0 {
					b.Fatal("retention removed all rows")
				}
			}
		})
		b.Run(name+"/Sharding", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				shards := cached.extents.Splice(time.Minute, 0, 0, 100)
				if len(shards) == 0 {
					b.Fatal("sharding returned no extents")
				}
			}
		})
	}
}

func TestLargeCacheEnvelopeMemoryContract(t *testing.T) {
	result := benchmarkResult(10000)
	protoBytes, err := sqltypes.ResultToProto3(result).MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := marshalCachedQueryResult(&cachedQueryResult{
		result:  result,
		extents: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(599940, 0)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	maximum := len(protoBytes) + len(protoBytes)/4 + 1<<20
	if len(envelope) > maximum {
		t.Fatalf("cache envelope = %d bytes, maximum contract = %d", len(envelope), maximum)
	}
}

func BenchmarkMySQLLargeResultRetention(b *testing.B) {
	result := benchmarkResult(10000)
	plan := benchmarkPlan()
	extents := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(599940, 0)}}
	h := &protocolHandler{config: ProtocolConfig{RetentionPoints: 5000}}
	envelope, err := marshalCachedQueryResult(&cachedQueryResult{result: result, extents: extents})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		retained, retainedExtents, err := h.applyRetentionSorted(result, extents, plan, 0)
		if err != nil {
			b.Fatal(err)
		}
		if len(retained.Rows) != 5000 || len(retainedExtents) != 1 {
			b.Fatal("unexpected retained result")
		}
	}
	b.ReportMetric(float64(len(envelope)), "retained-cache-B")
}

func BenchmarkMySQLOPCHitComparison(b *testing.B) {
	result := benchmarkResult(1000)
	protoResult := sqltypes.ResultToProto3(result)
	encoded, err := protoResult.MarshalVT()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("DirectResultEncoding", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := protoResult.MarshalVT(); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(encoded)), "encoded-B")
	})
	b.Run("CacheHit", func(b *testing.B) {
		cacheClient := benchmarkMemoryCache(b, "mysql-benchmark-opc-local")
		h := &protocolHandler{config: ProtocolConfig{
			BackendName: "mysql-benchmark-opc-local", Cache: cacheClient,
		}}
		connection := &vtmysql.Conn{User: "benchmark"}
		session := &upstreamSession{database: "analytics", timeZone: "+00:00"}
		query := "SELECT 42"
		key := h.queryCacheKey(connection, session, "opc", query)
		h.storeCached(key, &cachedQueryResult{result: result})
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, status, err := h.executeObject(connection, session,
				query); err != nil || status != cachestatus.LookupStatusHit {
				b.Fatalf("cache hit status=%s err=%v", status, err)
			}
		}
		b.ReportMetric(float64(len(encoded)), "encoded-B")
	})
}

type benchmarkOriginHandler struct {
	testOriginHandler
	result *sqltypes.Result
}

func (h *benchmarkOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	return callback(h.result)
}

func startBenchmarkOrigin(b *testing.B, result *sqltypes.Result) vtmysql.ConnParams {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	handler := &benchmarkOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}, result: result,
	}
	server, err := vtmysql.NewFromListener(listener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		b.Fatal(err)
	}
	go server.Accept()
	b.Cleanup(server.Shutdown)
	address := listener.Addr().(*net.TCPAddr)
	return vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port, Uname: "origin", Pass: "origin-password",
	}
}

func startBenchmarkDeltaOrigin(b *testing.B) vtmysql.ConnParams {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	handler := &deltaOriginHandler{testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}}
	server, err := vtmysql.NewFromListener(listener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		b.Fatal(err)
	}
	go server.Accept()
	b.Cleanup(server.Shutdown)
	address := listener.Addr().(*net.TCPAddr)
	return vtmysql.ConnParams{
		Host: "127.0.0.1", Port: address.Port, Uname: "origin", Pass: "origin-password",
	}
}

func dpcBenchmarkQuery(tenant int, lower, upper int64) string {
	query := strings.NewReplacer(
		"1785542400", fmt.Sprintf("%d", lower),
		"1785628800", fmt.Sprintf("%d", upper),
		"count(*) AS trips", "count(*) AS value",
	).Replace(safeDateTimeQuery)
	return strings.Replace(query, "WHERE ", fmt.Sprintf("WHERE tenant_id = %d AND ", tenant), 1)
}

func startBenchmarkProtocol(b *testing.B, config ProtocolConfig) string {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	server, err := NewProtocolServer(config)
	if err != nil {
		b.Fatal(err)
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil {
			b.Error(serveErr)
		}
	}()
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			b.Error(err)
		}
	})
	return listener.Addr().String()
}

func startBenchmarkRoutedProtocol(b *testing.B, config ProtocolConfig,
	resolver backends.RouteResolver, targets map[string]ProtocolConfig,
) string {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	server, err := NewRoutedProtocolServer(config, resolver, targets)
	if err != nil {
		b.Fatal(err)
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil {
			b.Error(serveErr)
		}
	}()
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			b.Error(err)
		}
	})
	return listener.Addr().String()
}

func benchmarkConnect(b *testing.B, address, user, password string) *vtmysql.Conn {
	b.Helper()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		b.Fatal(err)
	}
	var portNumber int
	if _, err := fmt.Sscan(port, &portNumber); err != nil {
		b.Fatal(err)
	}
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: host, Port: portNumber, Uname: user, Pass: password,
	})
	if err != nil {
		b.Fatal(err)
	}
	return client
}

func benchmarkMemoryCache(b *testing.B, name string) cache.Cache {
	b.Helper()
	configuration := cacheoptions.New()
	configuration.Name = name
	configuration.Provider = cacheproviders.Memory
	memoryClient := cachememory.New(name, configuration)
	cacheClient := cachemanager.NewCache(memoryClient, cachemanager.CacheOptions{}, configuration)
	if err := cacheClient.Connect(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cacheClient.Close() })
	return cacheClient
}

func BenchmarkMySQLProtocol(b *testing.B) {
	const user, password = "benchmark", "benchmark-password"
	b.Run("HandshakeAuthentication", func(b *testing.B) {
		address := startBenchmarkProtocol(b, ProtocolConfig{
			BackendName: "mysql-benchmark-handshake", ProxyOnly: true,
			DownstreamUsers: map[string]string{user: password},
		})
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			client := benchmarkConnect(b, address, user, password)
			client.Close()
		}
	})
	b.Run("UserRouterHandshakeAuthentication", func(b *testing.B) {
		const userCount = 1000
		users := make(map[string]string, userCount)
		resolver := make(testRouteResolver, userCount)
		targetName := "mysql-benchmark-route-target"
		target := backends.RouteTarget{Backend: benchmarkBackend(b, targetName)}
		for i := range userCount {
			username := fmt.Sprintf("benchmark-%04d", i)
			users[username] = password
			resolver[username] = target
		}
		address := startBenchmarkRoutedProtocol(b, ProtocolConfig{
			BackendName: "mysql-benchmark-router", DownstreamUsers: users,
		}, resolver, map[string]ProtocolConfig{
			targetName: {BackendName: targetName, ProxyOnly: true},
		})
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			client := benchmarkConnect(b, address, "benchmark-0500", password)
			client.Close()
		}
	})
	b.Run("COMQueryDispatch", func(b *testing.B) {
		upstream := startBenchmarkOrigin(b, benchmarkResult(0))
		address := startBenchmarkProtocol(b, ProtocolConfig{
			BackendName: "mysql-benchmark-dispatch", ProxyOnly: true, Upstream: upstream,
			DownstreamUsers: map[string]string{user: password},
		})
		client := benchmarkConnect(b, address, user, password)
		b.Cleanup(client.Close)
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := client.ExecuteFetch("SELECT dispatch", vtmysql.FETCH_ALL_ROWS, true); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, rowCount := range []int{10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("DirectOrigin/rows_%d", rowCount), func(b *testing.B) {
			upstream := startBenchmarkOrigin(b, benchmarkResult(rowCount))
			client, err := vtmysql.Connect(context.Background(), &upstream)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(client.Close)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.ExecuteFetch("SELECT benchmark_rows", vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("ProxyOnlyStreaming/rows_%d", rowCount), func(b *testing.B) {
			upstream := startBenchmarkOrigin(b, benchmarkResult(rowCount))
			address := startBenchmarkProtocol(b, ProtocolConfig{
				BackendName: "mysql-benchmark-stream", ProxyOnly: true, Upstream: upstream,
				DownstreamUsers: map[string]string{user: password},
			})
			client := benchmarkConnect(b, address, user, password)
			b.Cleanup(client.Close)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.ExecuteFetch("SELECT benchmark_rows", vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("OPC", func(b *testing.B) {
		upstream := startBenchmarkOrigin(b, benchmarkResult(1000))
		cacheClient := benchmarkMemoryCache(b, "mysql-benchmark-opc-cache")
		address := startBenchmarkProtocol(b, ProtocolConfig{
			BackendName: "mysql-benchmark-opc", Upstream: upstream, Cache: cacheClient,
			CacheTTL: time.Hour, DownstreamUsers: map[string]string{user: password},
		})
		client := benchmarkConnect(b, address, user, password)
		b.Cleanup(client.Close)
		if _, err := client.ExecuteFetch("SELECT 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
			b.Fatal(err)
		}
		b.Run("Hit", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := client.ExecuteFetch("SELECT 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("Miss", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				query := fmt.Sprintf("SELECT 42 AS value_%d", i)
				if _, err := client.ExecuteFetch(query, vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
	b.Run("DPC", func(b *testing.B) {
		upstream := startBenchmarkDeltaOrigin(b)
		cacheClient := benchmarkMemoryCache(b, "mysql-benchmark-dpc-cache")
		address := startBenchmarkProtocol(b, ProtocolConfig{
			BackendName: "mysql-benchmark-dpc", Upstream: upstream, Cache: cacheClient,
			CacheTTL: time.Hour, DownstreamUsers: map[string]string{user: password},
		})
		client := benchmarkConnect(b, address, user, password)
		b.Cleanup(client.Close)
		const lower, middle, upper = int64(1785542400), int64(1785585600), int64(1785628800)
		hitQuery := dpcBenchmarkQuery(0, lower, upper)
		if _, err := client.ExecuteFetch(hitQuery, vtmysql.FETCH_ALL_ROWS, true); err != nil {
			b.Fatal(err)
		}
		b.Run("Hit", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := client.ExecuteFetch(hitQuery, vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("Miss", func(b *testing.B) {
			b.ReportAllocs()
			for i := 1; b.Loop(); i++ {
				if _, err := client.ExecuteFetch(dpcBenchmarkQuery(i, lower, upper),
					vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("PartialHit", func(b *testing.B) {
			b.ReportAllocs()
			for i := 1; b.Loop(); i++ {
				b.StopTimer()
				tenant := 1_000_000 + i
				if _, err := client.ExecuteFetch(dpcBenchmarkQuery(tenant, lower, middle),
					vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := client.ExecuteFetch(dpcBenchmarkQuery(tenant, lower, upper),
					vtmysql.FETCH_ALL_ROWS, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}

func BenchmarkMySQLConcurrentSessions(b *testing.B) {
	const user, password = "benchmark", "benchmark-password"
	upstream := startBenchmarkOrigin(b, benchmarkResult(100))
	address := startBenchmarkProtocol(b, ProtocolConfig{
		BackendName: "mysql-benchmark-concurrent", ProxyOnly: true, Upstream: upstream,
		DownstreamUsers: map[string]string{user: password},
	})
	latencies := make([]int64, b.N)
	var position atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			started := time.Now()
			client := benchmarkConnect(b, address, user, password)
			_, err := client.ExecuteFetch("SELECT benchmark_rows", vtmysql.FETCH_ALL_ROWS, true)
			client.Close()
			if err != nil {
				b.Error(err)
				return
			}
			index := position.Add(1) - 1
			latencies[index] = time.Since(started).Nanoseconds()
		}
	})
	b.StopTimer()
	latencies = latencies[:position.Load()]
	slices.Sort(latencies)
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100]), "p95-ns/session")
	}
}

func benchmarkBackend(b *testing.B, name string) backends.Backend {
	b.Helper()
	options := bo.New()
	options.Name = name
	options.Provider = providers.MySQL
	options.OriginURL = "mysql://origin:password@example.com/database"
	backend, err := backends.New(name, options, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	return backend
}
