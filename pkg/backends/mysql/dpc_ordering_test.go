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
	"errors"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/collations"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
)

const nullGroupMarker = "<null>"

// dpcOrderingPlan groups a single column at a single timestamp, so the only
// thing that can order the rows is the group comparator.
func dpcOrderingPlan() *sqlanalyzer.QueryPlan {
	return &sqlanalyzer.QueryPlan{
		OutputColumn: "time", GroupColumns: []string{"grp"},
		OutputUnit: timeseries.DateTimeUnixSecs,
	}
}

func dpcOrderingResult(groupType querypb.Type, charset collations.ID,
	groups []sqltypes.Value,
) *sqltypes.Result {
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "grp", Type: groupType, Charset: uint32(charset)},
		{Name: "value", Type: querypb.Type_INT64},
	}
	rows := make([][]sqltypes.Value, len(groups))
	for i, group := range groups {
		rows[i] = []sqltypes.Value{
			sqltypes.NewInt64(0), group, sqltypes.NewInt64(int64(i)),
		}
	}
	return &sqltypes.Result{Fields: fields, Rows: rows}
}

func throughDelta(plan *sqlanalyzer.QueryPlan, parts ...*sqltypes.Result) (*sqltypes.Result, error) {
	// renders the parts' joined rows as a cached delta is rendered
	d, err := dpcTestHandler.deltaOf(plan, parts...)
	if err != nil {
		return nil, err
	}
	return dpcTestHandler.deltaResult(d, plan)
}

func groupOrder(rows [][]sqltypes.Value) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		if row[1].IsNull() {
			out[i] = nullGroupMarker
			continue
		}
		out[i] = row[1].ToString()
	}
	return out
}

// TestGroupOrderingUsesMySQLComparison pins the ordering of rows that share a
// timestamp. Raw serialized identity cannot express these MySQL orders.
func TestGroupOrderingUsesMySQLComparison(t *testing.T) {
	for _, tc := range []struct {
		name      string
		groupType querypb.Type
		charset   collations.ID
		input     []sqltypes.Value
		want      []string
	}{
		{
			// The review's headline case. The length prefix sorts "1:z" before
			// "2:aa"; MySQL's binary ascending order is aa, z.
			name:      "varbinary ignores byte length",
			groupType: querypb.Type_VARBINARY,
			charset:   collations.CollationBinaryID,
			input: []sqltypes.Value{
				sqltypes.NewVarBinary("z"), sqltypes.NewVarBinary("aa"),
			},
			want: []string{"aa", "z"},
		},
		{
			// Binary comparison stays case-sensitive and bytewise.
			name:      "varbinary stays bytewise",
			groupType: querypb.Type_VARBINARY,
			charset:   collations.CollationBinaryID,
			input: []sqltypes.Value{
				sqltypes.NewVarBinary("a"), sqltypes.NewVarBinary("B"),
			},
			want: []string{"B", "a"},
		},
		{
			// utf8mb4_0900_ai_ci is accent- and case-insensitive, so 'a' sorts
			// before 'B' even though 0x42 < 0x61.
			name:      "text uses its field collation",
			groupType: querypb.Type_VARCHAR,
			charset:   utf8mb40900AICI,
			input: []sqltypes.Value{
				sqltypes.NewVarChar("B"), sqltypes.NewVarChar("a"),
			},
			want: []string{"a", "B"},
		},
		{
			// The same values under a case-sensitive collation keep code-point
			// order, proving the field's collation is what decides.
			name:      "text under a binary collation",
			groupType: querypb.Type_VARCHAR,
			charset:   collations.CollationBinaryID,
			input: []sqltypes.Value{
				sqltypes.NewVarChar("a"), sqltypes.NewVarChar("B"),
			},
			want: []string{"B", "a"},
		},
		{
			// "-5" is longer than "3", so the length prefix ordered 3 first.
			name:      "signed integers order numerically",
			groupType: querypb.Type_INT64,
			charset:   collations.CollationBinaryID,
			input: []sqltypes.Value{
				sqltypes.NewInt64(3), sqltypes.NewInt64(-5), sqltypes.NewInt64(10),
			},
			want: []string{"-5", "3", "10"},
		},
		{
			name:      "decimals order numerically",
			groupType: querypb.Type_DECIMAL,
			charset:   collations.CollationBinaryID,
			input: []sqltypes.Value{
				sqltypes.NewDecimal("10.5"), sqltypes.NewDecimal("9.25"),
			},
			want: []string{"9.25", "10.5"},
		},
		{
			// MySQL sorts NULL first in ascending order.
			name:      "null sorts first",
			groupType: querypb.Type_VARCHAR,
			charset:   utf8mb40900AICI,
			input: []sqltypes.Value{
				sqltypes.NewVarChar("a"), sqltypes.NULL, sqltypes.NewVarChar(""),
			},
			want: []string{nullGroupMarker, "", "a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := dpcOrderingPlan()
			input := dpcOrderingResult(tc.groupType, tc.charset, tc.input)
			// the origin's order within a bucket plays no part: reversed input orders the same way
			for _, rows := range [][][]sqltypes.Value{input.Rows, reversed(input.Rows)} {
				out, err := throughDelta(plan, &sqltypes.Result{Fields: input.Fields, Rows: rows})
				if err != nil {
					t.Fatal(err)
				}
				if got := groupOrder(out.Rows); !equalStrings(got, tc.want) {
					t.Fatalf("order = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func reversed(rows [][]sqltypes.Value) [][]sqltypes.Value {
	out := slices.Clone(rows)
	slices.Reverse(out)
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMergeKeepsMySQLDistinctGroups(t *testing.T) {
	// binary groups that differ only in case are two groups in one bucket
	input := dpcOrderingResult(querypb.Type_VARBINARY, collations.CollationBinaryID,
		[]sqltypes.Value{sqltypes.NewVarBinary("a"), sqltypes.NewVarBinary("A")})
	out, err := throughDelta(dpcOrderingPlan(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got := groupOrder(out.Rows); !equalStrings(got, []string{"A", "a"}) {
		t.Fatalf("groups = %v", got)
	}
}

func TestRefetchedBucketsKeepOnlyTheOriginsNewestRows(t *testing.T) {
	// a volatile bucket is never stored, so when fetched again the origin's newest representative of a
	// case-insensitive group replaces the older one rather than joining it
	volatile := time.Since(time.Unix(0, 0))
	for name, configure := range map[string]func(*ProtocolConfig){
		"volatile window":        func(config *ProtocolConfig) { config.VolatileWindow = volatile },
		"volatile window points": func(config *ProtocolConfig) { config.VolatileWindowPoints = int(volatile/time.Minute) + 2 },
	} {
		t.Run(name, func(t *testing.T) {
			origin, _, client := startLifecycleProxy(t, "mysql-dpc-refetch", time.Second,
				func(config *ProtocolConfig) {
					config.ProxyOnly, config.Cache, config.CacheTTL = false, newTestCache(), time.Hour
					configure(config)
				})
			var representative atomic.Value
			origin.setResponder(func(string) *sqltypes.Result {
				return refetchResult(representative.Load().(string), 60)
			})
			for _, want := range []string{"a", "A"} {
				representative.Store(want)
				result, err := client.ExecuteFetch(refetchQuery, vtmysql.FETCH_ALL_ROWS, true)
				if err != nil {
					t.Fatal(err)
				}
				if got := groupOrder(result.Rows); !equalStrings(got, []string{want}) {
					t.Fatalf("groups = %v, want only %q", got, want)
				}
			}
			if got := origin.statementCount("events"); got != 2 {
				t.Fatalf("origin queries = %d, want one per request", got)
			}
		})
	}
}

func TestRetainedRowsLeaveTheResponseWhole(t *testing.T) {
	// retention trims what is cached, never the response, so the next request refetches the rest
	origin, _, client := startLifecycleProxy(t, "mysql-dpc-retention", time.Second,
		func(config *ProtocolConfig) {
			config.ProxyOnly, config.Cache, config.CacheTTL = false, newTestCache(), time.Hour
			config.RetentionPoints = 1
		})
	origin.setResponder(func(string) *sqltypes.Result { return refetchResult("a", 60, 120) })
	for range 2 {
		result, err := client.ExecuteFetch(retentionQuery, vtmysql.FETCH_ALL_ROWS, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Rows) != 2 {
			t.Fatalf("response rows = %d, want both buckets", len(result.Rows))
		}
	}
	if got := origin.statementCount("events"); got != 2 {
		t.Fatalf("origin queries = %d, want the retained bucket's refetch", got)
	}
}

func refetchResult(group string, epochs ...int64) *sqltypes.Result {
	input := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI, nil)
	for _, at := range epochs {
		input.Rows = append(input.Rows, []sqltypes.Value{
			sqltypes.NewInt64(at), sqltypes.NewVarChar(group), sqltypes.NewInt64(1),
		})
	}
	return input
}

const (
	refetchQuery = `SELECT
  cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time,
  grp AS grp,
  count(*) AS value
FROM events
WHERE ts >= FROM_UNIXTIME(60) AND ts < FROM_UNIXTIME(120)
GROUP BY time, grp
ORDER BY time, grp`
	retentionQuery = `SELECT
  cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time,
  grp AS grp,
  count(*) AS value
FROM events
WHERE ts >= FROM_UNIXTIME(60) AND ts < FROM_UNIXTIME(180)
GROUP BY time, grp
ORDER BY time, grp`
)

// TestGroupOrderingRejectsUnorderableColumns asserts DPC declines to order what
// it cannot order exactly. Each of these falls back to the object cache, which
// costs optimization rather than correctness.
func TestGroupOrderingRejectsUnorderableColumns(t *testing.T) {
	for _, tc := range []struct {
		name      string
		groupType querypb.Type
		values    []sqltypes.Value
		wantErr   string
	}{
		{
			// MySQL orders ENUM by declaration ordinal, which the result header
			// does not carry. Comparing the strings would order "large" before
			// "small" when the declaration says otherwise.
			name: "enum", groupType: querypb.Type_ENUM, wantErr: "declaration values",
			values: []sqltypes.Value{
				sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("small")),
				sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("large")),
			},
		},
		{
			// SET orders by member bitmask, likewise absent from the header.
			name: "set", groupType: querypb.Type_SET, wantErr: "declaration values",
			values: []sqltypes.Value{
				sqltypes.MakeTrusted(querypb.Type_SET, []byte("a,b")),
				sqltypes.MakeTrusted(querypb.Type_SET, []byte("b")),
			},
		},
		{
			// TIME can be negative, so its rendering is not byte-orderable.
			name: "time", groupType: querypb.Type_TIME, wantErr: "cannot order",
			values: []sqltypes.Value{
				sqltypes.MakeTrusted(querypb.Type_TIME, []byte("10:00:00")),
				sqltypes.MakeTrusted(querypb.Type_TIME, []byte("-01:00:00")),
			},
		},
		{
			name: "json", groupType: querypb.Type_JSON, wantErr: "cannot order",
			values: []sqltypes.Value{
				sqltypes.MakeTrusted(querypb.Type_JSON, []byte(`{"a":1}`)),
				sqltypes.MakeTrusted(querypb.Type_JSON, []byte(`{"a":2}`)),
			},
		},
		{
			name: "geometry", groupType: querypb.Type_GEOMETRY, wantErr: "cannot order",
			values: []sqltypes.Value{
				sqltypes.MakeTrusted(querypb.Type_GEOMETRY, []byte("\x00\x01")),
				sqltypes.MakeTrusted(querypb.Type_GEOMETRY, []byte("\x00\x02")),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := dpcOrderingPlan()
			input := dpcOrderingResult(tc.groupType, collations.CollationBinaryID, tc.values)
			_, err := throughDelta(plan, input)
			if !errors.Is(err, nativedelta.ErrUnmergeable) {
				t.Fatalf("ordered an unorderable %v column: %v", tc.groupType, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestUnmergeableDeltaPlanStopsRepeatingTheDeltaFetch asserts that a plan whose
// results can never be merged is recorded, so later requests degrade straight to
// the object cache instead of fetching every delta extent and discarding it.
const unorderableTierQuery = `SELECT
  cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time,
  tier AS tier,
  count(*) AS value
FROM events
WHERE ts >= FROM_UNIXTIME(0) AND ts < FROM_UNIXTIME(180)
GROUP BY time, tier
ORDER BY time, tier`

func unorderableTierResult(string) *sqltypes.Result {
	// the plan groups by an ENUM column, which DPC cannot order because the result header does not carry
	// the declaration values
	return &sqltypes.Result{
		Fields: []*querypb.Field{
			{Name: "time", Type: querypb.Type_INT64},
			{Name: "tier", Type: querypb.Type_ENUM, Charset: uint32(utf8mb40900AICI)},
			{Name: "value", Type: querypb.Type_INT64},
		},
		Rows: [][]sqltypes.Value{
			{
				sqltypes.NewInt64(0),
				sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("small")),
				sqltypes.NewInt64(1),
			},
			{
				sqltypes.NewInt64(60),
				sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("large")),
				sqltypes.NewInt64(2),
			},
		},
	}
}

func TestUnmergeableDeltaPlanStopsRepeatingTheDeltaFetch(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-dpc-fallback", time.Second,
		func(config *ProtocolConfig) {
			config.ProxyOnly = false
			config.Cache = newTestCache()
			config.CacheTTL = time.Hour
		})
	origin.setResponder(unorderableTierResult)
	const query = unorderableTierQuery

	if _, err := client.ExecuteFetch(query, vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	// The first request pays for the delta attempt plus the object fetch.
	attempted := origin.statementCount("events")
	if attempted < 2 {
		t.Fatalf("origin queries for the first request = %d, want the delta attempt "+
			"and the object fetch", attempted)
	}
	if _, err := client.ExecuteFetch(query, vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if got := origin.statementCount("events"); got != attempted {
		t.Fatalf("origin queries after the recorded fallback = %d, want %d: the delta "+
			"attempt repeated instead of reading the object entry", got, attempted)
	}
}

func TestOffNeverServesAnObjectStoredForTheDefaultTTL(t *testing.T) {
	const backend = "mysql-off-fallback"
	cache := newTestCache()
	shared := func(mode timeseries.StepAlignment) func(*ProtocolConfig) {
		return func(config *ProtocolConfig) {
			config.ProxyOnly, config.Cache, config.CacheTTL = false, cache, time.Hour
			config.StepAlignment = mode
		}
	}
	// the unorderable plan falls back to an object of the raw statement, stored for CacheTTL
	origin, _, client := startLifecycleProxy(t, backend, time.Second, shared(0))
	origin.setResponder(unorderableTierResult)
	if _, err := client.ExecuteFetch(unorderableTierQuery, vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	offOrigin, _, offClient := startLifecycleProxy(t, backend, time.Second, shared(timeseries.StepAlignmentOff))
	offOrigin.setResponder(unorderableTierResult)
	for range 2 {
		if _, err := offClient.ExecuteFetch(unorderableTierQuery, vtmysql.FETCH_ALL_ROWS, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := offOrigin.statementCount("events"); got != 1 {
		t.Fatalf("off reached the origin %d times, want once: never the fallback's object, then its own", got)
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	offEntries := 0
	for _, ttl := range cache.ttls {
		if ttl == timeseries.StepAlignmentOffTTL {
			offEntries++
		}
	}
	if offEntries != 1 {
		t.Errorf("expected one object stored for %s, got %d", timeseries.StepAlignmentOffTTL, offEntries)
	}
}

func TestMergeRejectsCollationChangeBetweenParts(t *testing.T) {
	// parts whose group column changed collation are not merged, since each would be ordered by one
	plan := dpcOrderingPlan()
	first := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI,
		[]sqltypes.Value{sqltypes.NewVarChar("a")})
	second := dpcOrderingResult(querypb.Type_VARCHAR, latin1SwedishCI,
		[]sqltypes.Value{sqltypes.NewVarChar("B")})
	second.Rows[0][0] = sqltypes.NewInt64(60)
	if _, err := throughDelta(plan, first, second); err == nil {
		t.Fatal("accepted parts whose group collation changed")
	}
	// The same collation on both parts still merges.
	same := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI,
		[]sqltypes.Value{sqltypes.NewVarChar("B")})
	same.Rows[0][0] = sqltypes.NewInt64(60)
	if _, err := throughDelta(plan, first, same); err != nil {
		t.Fatalf("rejected parts sharing one collation: %v", err)
	}
}

// TestGroupOrderingValidatesValueTypes asserts a value that does not carry its
// field's declared type is rejected rather than ordered by the wrong rule.
func TestGroupOrderingValidatesValueTypes(t *testing.T) {
	plan := dpcOrderingPlan()
	// The field declares VARCHAR, so the comparator selected a collation; the
	// row holds an integer.
	input := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI, []sqltypes.Value{
		sqltypes.NewVarChar("a"), sqltypes.NewInt64(7),
	})
	_, err := throughDelta(plan, input)
	if !errors.Is(err, nativedelta.ErrUnmergeable) {
		t.Fatalf("accepted a group value of the wrong type: %v", err)
	}
	if !strings.Contains(err.Error(), "want VARCHAR") {
		t.Fatalf("error = %v, want it to name the declared type", err)
	}
}

// TestGroupOrderingPropagatesComparisonErrors asserts an unusable collation
// surfaces instead of silently falling back to a byte order the origin would
// not have produced.
func TestGroupOrderingPropagatesComparisonErrors(t *testing.T) {
	plan := dpcOrderingPlan()
	values := []sqltypes.Value{sqltypes.NewVarChar("b"), sqltypes.NewVarChar("a")}

	t.Run("unimplemented collation", func(t *testing.T) {
		// A collation ID Vitess does not implement.
		const unknownCollation = collations.ID(1023)
		if dpcTestHandler.collationEnv().IsSupported(unknownCollation) {
			t.Skipf("collation %d is supported; pick an unimplemented ID", unknownCollation)
		}
		input := dpcOrderingResult(querypb.Type_VARCHAR, unknownCollation, values)
		if _, err := throughDelta(plan, input); err == nil {
			t.Fatal("silently ordered rows under an unusable collation")
		}
	})

	t.Run("charset overflows collations.ID", func(t *testing.T) {
		// Truncating this to uint16 would wrap to Unknown and order under the
		// connection default instead of rejecting the column.
		input := dpcOrderingResult(querypb.Type_VARCHAR, 0, values)
		input.Fields[1].Charset = uint32(math.MaxUint16) + 1
		if _, err := throughDelta(plan, input); err == nil {
			t.Fatal("truncated an out-of-range collation to Unknown")
		}
	})
}

func TestGroupOrderingAcrossSparseBuckets(t *testing.T) {
	plan := dpcOrderingPlan()
	plan.Step = time.Minute
	input := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI, nil)
	// "c" is only in the first bucket and "a" only in the second, so neither bucket holds every series
	for _, row := range []struct {
		at    int64
		group string
	}{{0, "c"}, {0, "B"}, {60, "B"}, {60, "a"}} {
		input.Rows = append(input.Rows, []sqltypes.Value{
			sqltypes.NewInt64(row.at), sqltypes.NewVarChar(row.group), sqltypes.NewInt64(1),
		})
	}
	d, err := dpcTestHandler.deltaOf(plan, input)
	if err != nil {
		t.Fatal(err)
	}
	// series without rows yield none, wherever they are
	r := d.DS.Results[0]
	r.SeriesList = append(slices.Insert(r.SeriesList, 0, nil), dataset.NewSeries(dataset.SeriesHeader{}, nil))
	got, err := dpcTestHandler.deltaResult(d, plan)
	if err != nil {
		t.Fatal(err)
	}
	if order := groupOrder(got.Rows); !slices.Equal(order, []string{"B", "c", "a", "B"}) {
		t.Fatalf("rows in order %v", order)
	}
}

func TestRenderBuffersRelease(t *testing.T) {
	(*renderBuffers)(nil).release()
	// a buffer too large to keep is cleared but not kept
	large := &renderBuffers{values: make([]sqltypes.Value, maxPooledRender/16)}
	large.values[0] = sqltypes.NewVarChar("cached")
	large.release()
	if !large.values[0].IsNull() || len(large.values) != maxPooledRender/16 {
		t.Fatal("a dropped buffer kept its values or was kept")
	}
	small := &renderBuffers{values: make([]sqltypes.Value, 4), rows: make([]sqltypes.Row, 1, 2)}
	small.release()
	if len(small.values) != 0 || len(small.rows) != 0 {
		t.Fatal("a small buffer was not kept for reuse")
	}
}
