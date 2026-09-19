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

package pgwire

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	cacheTestSelect = "SELECT date_bin(INTERVAL '5 minutes', ts, TIMESTAMP '2000-01-01') AS time, count(*) AS value FROM trips "
	cacheTestHosts  = "SELECT date_bin(INTERVAL '5 minutes', ts, TIMESTAMP '2000-01-01') AS time, host, count(*) AS value FROM trips "
	cacheTestDay    = "2026-09-10T"
)

type byteCache struct {
	mtx  sync.Mutex
	data map[string][]byte
}

func newByteCache() *byteCache { return &byteCache{data: make(map[string][]byte)} }

func (c *byteCache) Connect() error { return nil }
func (c *byteCache) Close() error   { return nil }

func (c *byteCache) Configuration() *cacheoptions.Options { return cacheoptions.New() }

func (c *byteCache) Store(key string, data []byte, _ time.Duration) error {
	c.mtx.Lock()
	c.data[key] = append([]byte(nil), data...)
	c.mtx.Unlock()
	return nil
}

func (c *byteCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	c.mtx.Lock()
	data, ok := c.data[key]
	c.mtx.Unlock()
	if !ok {
		return nil, status.LookupStatusKeyMiss, trickstercache.ErrKNF
	}
	return append([]byte(nil), data...), status.LookupStatusHit, nil
}

func (c *byteCache) Remove(keys ...string) error {
	c.mtx.Lock()
	for _, key := range keys {
		delete(c.data, key)
	}
	c.mtx.Unlock()
	return nil
}

func cachedConfig(t *testing.T, f *fakeUpstream) Config {
	t.Helper()
	c := gatedConfig(t, f)
	c.Engine, c.Cache, c.CacheTTL = testEngine{}, newByteCache(), time.Hour
	c.MaxResultRows, c.MaxResultSizeBytes = 100000, 1<<26
	return c
}

func rangeQuery(selectList, from, to, tail string) string {
	return fmt.Sprintf("%sWHERE ts >= '%s%s:00Z' AND ts < '%s%s:00Z' GROUP BY %s", selectList,
		cacheTestDay, from, cacheTestDay, to, tail)
}

func rowsOf(t *testing.T, conn *pgconn.PgConn, sql string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	results, err := conn.Exec(ctx, sql).ReadAll()
	if err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	var rows []string
	for _, result := range results {
		for _, row := range result.Rows {
			cells := make([]string, len(row))
			for i, cell := range row {
				cells[i] = string(cell)
			}
			rows = append(rows, strings.Join(cells, "|"))
		}
		rows = append(rows, "tag:"+result.CommandTag.String())
	}
	return rows
}

func cacheCount(backend string, mode sqlanalyzer.CacheMode, lookup status.LookupStatus) float64 {
	return testutil.ToFloat64(metrics.SQLQueryCache.WithLabelValues(backend, testProvider, mode.String(), lookup.String()))
}

func directRows(t *testing.T, upstream *fakeUpstream, sql string) []string {
	t.Helper()
	_, address := startServer(t, testConfig(upstream))
	return rowsOf(t, mustDial(t, address, testClientUser, testClientPass), sql)
}

func TestDeltaCacheFetchesOnlyWhatIsMissing(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	first := rangeQuery(cacheTestSelect, "08:00", "11:00", "1 ORDER BY 1")
	shifted := rangeQuery(cacheTestSelect, "09:00", "12:00", "1 ORDER BY 1")
	wantFirst, wantShifted := directRows(t, upstream, first), directRows(t, upstream, shifted)
	upstream.forget()

	if got := rowsOf(t, conn, first); !equalRows(got, wantFirst) || len(got) != 37 {
		t.Fatalf("cold miss differs from the origin:\n%v\n%v", got, wantFirst)
	}
	if got := upstream.received(); len(got) != 1 || !strings.Contains(got[0], "08:00:00Z") || !strings.Contains(got[0], "11:00:00Z") {
		t.Fatalf("expected one fetch of the whole range, got %q", got)
	}
	if got := rowsOf(t, conn, first); !equalRows(got, wantFirst) {
		t.Fatalf("full hit differs from the origin:\n%v\n%v", got, wantFirst)
	}
	if got := upstream.received(); len(got) != 1 {
		t.Fatalf("a full hit must not reach the origin, got %q", got)
	}
	if got := rowsOf(t, conn, shifted); !equalRows(got, wantShifted) {
		t.Fatalf("partial hit differs from the origin:\n%v\n%v", got, wantShifted)
	}
	got := upstream.received()
	if len(got) != 2 || !strings.Contains(got[1], "11:00:00Z") || !strings.Contains(got[1], "12:00:00Z") ||
		strings.Contains(got[1], "09:00:00Z") {
		t.Fatalf("expected only the missing hour to be fetched, got %q", got)
	}
	for lookup, want := range map[status.LookupStatus]float64{
		status.LookupStatusKeyMiss: 1, status.LookupStatusHit: 1, status.LookupStatusPartialHit: 1,
	} {
		if got := cacheCount(config.BackendName, sqlanalyzer.CacheModeDelta, lookup); got != want {
			t.Fatalf("%s: counted %v, want %v", lookup, got, want)
		}
	}
	// the session is still an ordinary one afterwards
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("a relayed statement after cached ones: %d rows, %v", rows, err)
	}
}

func equalRows(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

func TestDeltaCacheKeepsTheOriginsOrderWithinABucket(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, cachedConfig(t, upstream))
	conn := mustDial(t, address, testClientUser, testClientPass)
	for name, tail := range map[string]string{
		"ascending": "1, 2 ORDER BY 1", "descending": "1, 2 ORDER BY 1 DESC", "unordered": "1, 2",
	} {
		first := rangeQuery(cacheTestHosts, "08:00", "09:00", tail)
		wider := rangeQuery(cacheTestHosts, "07:30", "09:30", tail)
		want := directRows(t, upstream, wider)
		rowsOf(t, conn, first)
		upstream.forget()
		// the wider range merges cached buckets with two freshly fetched edges
		if got := rowsOf(t, conn, wider); !equalRows(got, want) {
			t.Fatalf("%s: merged result differs from the origin:\n%v\n%v", name, got, want)
		}
		if got := upstream.received(); len(got) != 2 {
			t.Fatalf("%s: expected the two missing edges to be fetched, got %q", name, got)
		}
	}
	// ordering by a group column first cannot be rebuilt from buckets, so it is an object
	grouped := rangeQuery(cacheTestHosts, "08:00", "09:00", "1, 2 ORDER BY 2, 1")
	want := directRows(t, upstream, grouped)
	upstream.forget()
	for range 2 {
		if got := rowsOf(t, conn, grouped); !equalRows(got, want) {
			t.Fatalf("object result differs from the origin:\n%v\n%v", got, want)
		}
	}
	if got := upstream.received(); len(got) != 1 || got[0] != grouped {
		t.Fatalf("expected the statement itself to be fetched once, got %q", got)
	}
}

func TestObjectCacheServesRepeatedStatements(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	other := mustDial(t, address, testClientUser, testClientPass)
	want := directRows(t, upstream, gateObjectQuery)
	upstream.forget()
	for _, c := range []*pgconn.PgConn{conn, conn, other} {
		if got := rowsOf(t, c, gateObjectQuery); !equalRows(got, want) {
			t.Fatalf("object result differs from the origin:\n%v\n%v", got, want)
		}
	}
	if got := upstream.received(); len(got) != 1 {
		t.Fatalf("sessions with one identity must share one fetch, got %q", got)
	}
	if got := cacheCount(config.BackendName, sqlanalyzer.CacheModeObject, status.LookupStatusHit); got != 2 {
		t.Fatalf("counted %v hits, want 2", got)
	}
}

func TestCachePartitionsBySessionIdentity(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, cachedConfig(t, upstream))
	utc := mustDial(t, address, testClientUser, testClientPass)
	eastern := mustDial(t, address, testClientUser, testClientPass)
	if _, err := queryRows(t, eastern, fakeQuerySetZone+"'"+gateZoneNewYork+"'"); err != nil {
		t.Fatal(err)
	}
	upstream.forget()
	rowsOf(t, utc, gateObjectQuery)
	rowsOf(t, eastern, gateObjectQuery)
	if got := upstream.received(); len(got) != 2 {
		t.Fatalf("a different time zone must not share a cached result, got %q", got)
	}
}

func TestDeltaCacheRefetchesTheVolatileTail(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	config.BackfillWindow = 30 * time.Minute
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	now := time.Now().UTC().Truncate(fakeBucketStep)
	sql := fmt.Sprintf("%sWHERE ts >= '%s' AND ts < '%s' GROUP BY 1 ORDER BY 1", cacheTestSelect,
		now.Add(-2*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	first := rowsOf(t, conn, sql)
	upstream.forget()
	if got := rowsOf(t, conn, sql); !equalRows(got, first) {
		t.Fatalf("the refreshed result differs:\n%v\n%v", got, first)
	}
	got := upstream.received()
	if len(got) != 1 || strings.Contains(got[0], now.Add(-2*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("expected only the recent, still-changing buckets to be refetched, got %q", got)
	}
}

func TestOpenEndedRangeRunsToNow(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, cachedConfig(t, upstream))
	conn := mustDial(t, address, testClientUser, testClientPass)
	now := time.Now().UTC().Truncate(fakeBucketStep)
	sql := fmt.Sprintf("%sWHERE ts >= '%s' GROUP BY 1 ORDER BY 1", cacheTestSelect,
		now.Add(-time.Hour).Format(time.RFC3339))
	if got := rowsOf(t, conn, sql); len(got) < 12 {
		t.Fatalf("expected about an hour of buckets, got %v", got)
	}
	upstream.forget()
	rowsOf(t, conn, sql)
	if got := upstream.received(); len(got) != 1 || strings.Contains(got[0], now.Add(-time.Hour).Format(time.RFC3339)) {
		t.Fatalf("expected only the still-filling bucket to be refetched, got %q", got)
	}
}

func TestRejectedRewriteFallsBackToTheClientsStatement(t *testing.T) {
	var original string
	upstream := newFakeUpstream(t, func(f *fakeUpstream) {
		// this origin cannot read Trickster's rewritten statement, only the client's own
		f.reject = func(sql string) bool { return strings.Contains(sql, fakeBucketFunction) && sql != original }
	})
	config := cachedConfig(t, upstream)
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	original = rangeQuery(cacheTestSelect, "08:00", "09:00", "1 ORDER BY 1")
	failures := func() float64 {
		return testutil.ToFloat64(metrics.SQLQueryRewriteFailures.WithLabelValues(config.BackendName, testProvider, rewriteOriginRejected))
	}
	if got := rowsOf(t, conn, original); len(got) != 13 {
		t.Fatalf("expected the client's own statement to be answered, got %v", got)
	}
	if got := upstream.received(); len(got) != 2 || got[1] != original || failures() != 1 {
		t.Fatalf("expected the rewrite, then the original, got %q (failures %v)", got, failures())
	}
	upstream.forget()
	if got := rowsOf(t, conn, original); len(got) != 13 {
		t.Fatalf("got %v", got)
	}
	if got := upstream.received(); len(got) != 1 || got[0] != original || failures() != 1 {
		t.Fatalf("a rejected plan must not be retried while its marker lives, got %q", got)
	}
}

func TestOriginErrorsReachTheClient(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, cachedConfig(t, upstream))
	conn := mustDial(t, address, testClientUser, testClientPass)
	for range 2 {
		if _, err := queryRows(t, conn, fakeQueryError); sqlstate(err) != sqlstateDivByZero {
			t.Fatalf("expected the origin's error, got %v", err)
		}
	}
	if got := upstream.received(); len(got) != 2 {
		t.Fatalf("an error must never be cached, got %q", got)
	}
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("the session must survive: %d rows, %v", rows, err)
	}
	// a cancel reaches a statement that is being fetched for the cache
	cancelRunningQuery(t, conn, upstream, legacySecretLen)
}

func TestOversizedResultsAreRelayedNotCached(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	config.MaxResultRows = 10
	server, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	proxied := testutil.ToFloat64(server.proxied.requests)
	for range 2 {
		// the client's own statement outgrows the limit partway through
		if rows, err := queryRows(t, conn, fakeQueryMany); err != nil || rows != fakeManyRows {
			t.Fatalf("expected the whole result, got %d rows, %v", rows, err)
		}
	}
	if got := upstream.received(); len(got) != 2 {
		t.Fatalf("an oversized result is fetched once per request, never twice: %q", got)
	}
	if got := testutil.ToFloat64(server.proxied.requests) - proxied; got != 2 {
		t.Fatalf("expected both requests to complete through the relay, got %v", got)
	}
	upstream.forget()
	sql := rangeQuery(cacheTestSelect, "08:00", "11:00", "1 ORDER BY 1")
	want := directRows(t, upstream, sql)
	upstream.forget()
	// a rewritten sub-query that outgrows the limit is abandoned for the original
	if got := rowsOf(t, conn, sql); !equalRows(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := upstream.received(); len(got) != 2 || got[1] != sql {
		t.Fatalf("expected the rewrite, then the original, got %q", got)
	}
}

func TestUnreadableTimeAxisFallsBackToTheObjectCache(t *testing.T) {
	for name, mutate := range map[string]func(*fakeUpstream){
		"type with no time axis": func(f *fakeUpstream) { f.timeOID = 25 },
		"integer without a unit": func(f *fakeUpstream) { f.timeOID = OIDInt4 },
		"non-ISO DateStyle":      func(f *fakeUpstream) { f.dateStyle = "German, DMY" },
	} {
		t.Run(name, func(t *testing.T) {
			upstream := newFakeUpstream(t, mutate)
			config := cachedConfig(t, upstream)
			_, address := startServer(t, config)
			conn := mustDial(t, address, testClientUser, testClientPass)
			sql := rangeQuery(cacheTestSelect, "08:00", "09:00", "1 ORDER BY 1")
			want := directRows(t, upstream, sql)
			upstream.forget()
			for range 3 {
				if got := rowsOf(t, conn, sql); !equalRows(got, want) {
					t.Fatalf("got %v\nwant %v", got, want)
				}
			}
			// one rewritten fetch proves the plan unmergeable; one object fetch serves the rest
			if got := upstream.received(); len(got) != 2 || got[1] != sql {
				t.Fatalf("expected a delta attempt and one object fetch, got %q", got)
			}
			if got := cacheCount(config.BackendName, sqlanalyzer.CacheModeDelta, status.LookupStatusHit); got != 2 {
				t.Fatalf("counted %v hits, want 2", got)
			}
		})
	}
}

func TestAsynchronousMessagesReachTheClientDuringAFetch(t *testing.T) {
	upstream := newFakeUpstream(t, func(f *fakeUpstream) { f.notice = true })
	_, address := startServer(t, cachedConfig(t, upstream))
	config, err := pgconn.ParseConfig("postgres://" + testClientUser + ":" + testClientPass + "@" + address + "/" + testDatabase + "?" + testSSLDisable)
	if err != nil {
		t.Fatal(err)
	}
	notices := make(chan string, 4)
	config.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) { notices <- notice.Message }
	ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
	defer cancel()
	conn, err := pgconn.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if got := rowsOf(t, conn, rangeQuery(cacheTestSelect, "08:00", "09:00", "1 ORDER BY 1")); len(got) != 13 {
		t.Fatalf("got %v", got)
	}
	select {
	case message := <-notices:
		if message != fakeNoticeText {
			t.Fatalf("unexpected notice %q", message)
		}
	default:
		t.Fatal("the origin's notice was swallowed by the fetch")
	}
}

func TestMemoryCacheKeepsTypedResults(t *testing.T) {
	configuration := cacheoptions.New()
	configuration.Name, configuration.Provider = "pgwire-reference-cache", cacheproviders.Memory
	client := cachemanager.NewCache(cachememory.New(configuration.Name, configuration), cachemanager.CacheOptions{}, configuration)
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	config.Cache = client
	config.RetentionPoints = 6
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	sql := rangeQuery(cacheTestSelect, "08:00", "10:00", "1 ORDER BY 1")
	want := directRows(t, upstream, sql)
	upstream.forget()
	for range 2 {
		if got := rowsOf(t, conn, sql); !equalRows(got, want) {
			t.Fatalf("got %v\nwant %v", got, want)
		}
	}
	// only the newest six buckets are kept, so the older ones are fetched again
	got := upstream.received()
	if len(got) != 2 || !strings.Contains(got[1], "08:00:00Z") || !strings.Contains(got[1], "09:30:00Z") {
		t.Fatalf("expected the buckets beyond retention to be refetched, got %q", got)
	}
}

func TestConcurrentSessionsShareTheCache(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	_, address := startServer(t, cachedConfig(t, upstream))
	sql := rangeQuery(cacheTestSelect, "08:00", "11:00", "1 ORDER BY 1")
	want := directRows(t, upstream, sql)
	upstream.forget()
	const sessions = 8
	var wg sync.WaitGroup
	failures := make(chan string, sessions)
	for range sessions {
		wg.Go(func() {
			conn, err := dial(t, address, testClientUser, testClientPass)
			if err != nil {
				failures <- err.Error()
				return
			}
			defer conn.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
			defer cancel()
			for range 5 {
				results, err := conn.Exec(ctx, sql).ReadAll()
				if err != nil || len(results) != 1 || len(results[0].Rows) != len(want)-1 {
					failures <- fmt.Sprintf("%v: %d results", err, len(results))
					return
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
	if got := upstream.received(); len(got) != 1 {
		t.Fatalf("forty identical requests must reach the origin once, got %d fetches", len(got))
	}
}

type cacheProviderFunc func() trickstercache.Cache

func (f cacheProviderFunc) Cache() trickstercache.Cache { return f() }

func TestShardedFetchesThroughACacheProvider(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	shared := config.Cache
	config.Cache = nil
	config.CacheProvider = cacheProviderFunc(func() trickstercache.Cache { return shared })
	config.DoesShard, config.ShardMaxRange, config.QueryTimeout = true, time.Hour, fakeTimeout
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	sql := rangeQuery(cacheTestSelect, "08:00", "11:00", "1 ORDER BY 1")
	want := directRows(t, upstream, sql)
	upstream.forget()
	if got := rowsOf(t, conn, sql); !equalRows(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := upstream.received(); len(got) != 3 {
		t.Fatalf("expected three hour-sized fetches, got %q", got)
	}
	if (&originError{code: sqlstateSyntaxError}).Error() == "" {
		t.Fatal("an origin error names its SQLSTATE")
	}
}

func TestCachingIsOffWithoutACache(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	config := cachedConfig(t, upstream)
	config.Cache = nil
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	for range 2 {
		rowsOf(t, conn, gateObjectQuery)
	}
	if got := upstream.received(); len(got) != 2 || got[0] != gateObjectQuery {
		t.Fatalf("with no cache every statement is relayed as written, got %q", got)
	}
}

func TestCacheMetricsStartAtZero(t *testing.T) {
	// a series born at 1 has no earlier sample, so rate() would miss the first partial hit after a restart
	upstream := newFakeUpstream(t, nil)
	config := gatedConfig(t, upstream)
	if _, err := NewServer(config); err != nil {
		t.Fatal(err)
	}
	exported := 0
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "trickster_sql_query_cache_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "backend_name" && label.GetValue() == config.BackendName {
					exported++
				}
			}
		}
	}
	want := 0
	for _, statuses := range primedCacheStatuses {
		want += len(statuses)
	}
	if exported != want {
		t.Fatalf("expected %d series at zero before any statement, got %d", want, exported)
	}
}
