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

package mysql

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtenv"
)

const (
	partialHost      = "host"
	partialBase      = 1785542400
	partialBucketTTL = 20 * time.Second
)

type partialOriginHandler struct {
	testOriginHandler
	mtx     sync.Mutex
	queries []string
}

func (h *partialOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	// aggregates as MySQL would: a row per minute bucket the raw range covers, valued by the seconds
	// covered, so a partial bucket's value shows how much was fetched
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	h.mtx.Lock()
	h.queries = append(h.queries, query)
	h.mtx.Unlock()
	plan := defaultAnalyzer.Analyze(query, time.Time{}).Plan
	if plan == nil {
		return fmt.Errorf("unexpected partial origin query: %s", query)
	}
	lower, upper := plan.RawLower.Value.Unix(), plan.RawUpper.Value.Unix()
	if plan.RawUpper.Inclusive {
		upper++
	}
	return callback(partialResult(lower, upper, strings.Contains(query, partialHost)))
}

func (h *partialOriginHandler) received() []string {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return append([]string(nil), h.queries...)
}

func partialResult(lower, upper int64, grouped bool) *sqltypes.Result {
	result := &sqltypes.Result{Fields: []*querypb.Field{{Name: "time", Type: querypb.Type_INT64}}}
	hosts := []string{""}
	if grouped {
		result.Fields = append(result.Fields,
			&querypb.Field{Name: partialHost, Type: querypb.Type_VARCHAR, Charset: uint32(utf8mb40900AICI)})
		hosts = []string{"a", "b"}
	}
	result.Fields = append(result.Fields, &querypb.Field{Name: "value", Type: querypb.Type_INT64})
	for bucket := lower - lower%60; bucket < upper; bucket += 60 {
		covered := min(bucket+60, upper) - max(bucket, lower)
		for i, host := range hosts {
			row := []sqltypes.Value{sqltypes.NewInt64(bucket)}
			if grouped {
				row = append(row, sqltypes.NewVarChar(host))
			}
			result.Rows = append(result.Rows, append(row, sqltypes.NewInt64(covered+int64(i)*1000)))
		}
	}
	return result
}

func partialQuery(lower, upper int64, grouped bool) string {
	if grouped {
		return fmt.Sprintf("SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time, host, count(*) AS value FROM events "+
			"WHERE ts >= FROM_UNIXTIME(%d) AND ts <= FROM_UNIXTIME(%d) GROUP BY time, host ORDER BY time, host",
			lower, upper)
	}
	return fmt.Sprintf("SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time, count(*) AS value FROM events "+
		"WHERE ts >= FROM_UNIXTIME(%d) AND ts <= FROM_UNIXTIME(%d) GROUP BY time ORDER BY time", lower, upper)
}

func startPartialServer(t *testing.T, mode timeseries.StepAlignment) (*vtmysql.Conn, *partialOriginHandler, *testCache) {
	t.Helper()
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := &partialOriginHandler{env: vtenv.NewTestEnv()}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	t.Cleanup(origin.Shutdown)
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cache := newTestCache()
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{
			Host: "127.0.0.1", Port: originListener.Addr().(*net.TCPAddr).Port,
			Uname: "origin", Pass: "origin-password",
		},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, BackendName: "mysql-partial-" + mode.String(),
		Cache: cache, CacheTTL: time.Hour, PartialBucketTTL: partialBucketTTL, StepAlignment: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(proxyListener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyListener.Addr().(*net.TCPAddr).Port,
		Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, handler, cache
}

func resultRows(result *sqltypes.Result) []string {
	rows := make([]string, len(result.Rows))
	for i, row := range result.Rows {
		cells := make([]string, len(row))
		for j, cell := range row {
			cells[j] = cell.ToString()
		}
		rows[i] = strings.Join(cells, "|")
	}
	return rows
}

func TestPartialBucketsMatchTheOrigin(t *testing.T) {
	// 30s past a minute to 7s past another, inclusive: each mode's answer is the origin's over the
	// range it serves
	lower, upper := int64(partialBase+30), int64(partialBase+607)
	for mode, served := range map[timeseries.StepAlignment][2]int64{
		timeseries.StepAlignmentTruncate:     {partialBase, partialBase + 600},
		timeseries.StepAlignmentDrop:         {partialBase + 60, partialBase + 600},
		timeseries.StepAlignmentPartial:      {lower, upper + 1},
		timeseries.StepAlignmentPartialStart: {lower, partialBase + 600},
		timeseries.StepAlignmentPartialEnd:   {partialBase, upper + 1},
	} {
		for _, grouped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/grouped=%t", mode, grouped), func(t *testing.T) {
				client, origin, cache := startPartialServer(t, mode)
				want := resultRows(partialResult(served[0], served[1], grouped))
				for _, attempt := range []string{"first", "repeat"} {
					got, err := client.ExecuteFetch(partialQuery(lower, upper, grouped), vtmysql.FETCH_ALL_ROWS, true)
					if err != nil {
						t.Fatal(err)
					}
					if rows := resultRows(got); strings.Join(rows, " ") != strings.Join(want, " ") {
						t.Fatalf("%s = %v, want %v", attempt, rows, want)
					}
				}
				// the interior once and each partial bucket once, bounded by the client's own range
				start, end := mode.Edges()
				partials := 0
				for _, edge := range []timeseries.EdgePolicy{start, end} {
					if edge == timeseries.EdgePartial {
						partials++
					}
				}
				received := origin.received()
				bounded := 0
				for _, q := range received {
					if strings.Contains(q, strconv.FormatInt(lower, 10)) || strings.Contains(q, strconv.FormatInt(upper, 10)) {
						bounded++
					}
				}
				if len(received) != partials+1 || bounded != partials {
					t.Fatalf("the origin received %q, want %d partial buckets", received, partials)
				}
				partialObjects := 0
				for key, ttl := range cache.ttls {
					if ttl == partialBucketTTL {
						partialObjects++
					} else if ttl != time.Hour {
						t.Errorf("%s stored for %s", key, ttl)
					}
				}
				if partialObjects != partials {
					t.Errorf("%d partial bucket objects, want %d", partialObjects, partials)
				}
				// bucket 600, whole now, answers its whole value, so no partial value was cached; the new inclusive
				// end is partial, which only the partial end modes serve
				wholeUpper := int64(partialBase + 660)
				if end == timeseries.EdgePartial {
					wholeUpper++
				}
				got, err := client.ExecuteFetch(partialQuery(partialBase, partialBase+660, grouped), vtmysql.FETCH_ALL_ROWS, true)
				if err != nil {
					t.Fatal(err)
				}
				rows, whole := resultRows(got), resultRows(partialResult(partialBase, wholeUpper, grouped))
				if strings.Join(rows, " ") != strings.Join(whole, " ") {
					t.Fatalf("a whole range after partial buckets = %v, want %v", rows, whole)
				}
			})
		}
	}
}

func TestPartialBucketStatementsKeepTheClientsBounds(t *testing.T) {
	client, origin, _ := startPartialServer(t, timeseries.StepAlignmentPartial)
	if _, err := client.ExecuteFetch(partialQuery(partialBase+30, partialBase+607, false), vtmysql.FETCH_ALL_ROWS,
		true); err != nil {
		t.Fatal(err)
	}
	// the start bucket ends a tick below the first whole bucket, through the statement's <=, and the end
	// bucket keeps the client's raw inclusive upper
	want := map[string]bool{
		fmt.Sprintf("FROM_UNIXTIME(%d) and ts <= FROM_UNIXTIME(%d)", partialBase+30, partialBase+59):   false,
		fmt.Sprintf("FROM_UNIXTIME(%d) and ts <= FROM_UNIXTIME(%d)", partialBase+600, partialBase+607): false,
	}
	for _, q := range origin.received() {
		for bounds := range want {
			if strings.Contains(q, bounds) {
				want[bounds] = true
			}
		}
	}
	for bounds, seen := range want {
		if !seen {
			t.Errorf("no statement held %q: %q", bounds, origin.received())
		}
	}
}
