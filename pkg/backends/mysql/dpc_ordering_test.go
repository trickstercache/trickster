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
	"math"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

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

			merged, err := dpcTestHandler.mergeResults([]*sqltypes.Result{input}, plan)
			if err != nil {
				t.Fatal(err)
			}
			if got := groupOrder(merged.Rows); !equalStrings(got, tc.want) {
				t.Fatalf("mergeResults order = %v, want %v", got, tc.want)
			}

			// cropAndSortResult must reach the same order from the same rows.
			cropped, err := dpcTestHandler.cropAndSortResult(input, plan,
				timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(1, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if got := groupOrder(cropped.Rows); !equalStrings(got, tc.want) {
				t.Fatalf("cropAndSortResult order = %v, want %v", got, tc.want)
			}
		})
	}
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

func TestMergeDeduplicatesUsingMySQLEquality(t *testing.T) {
	for _, tc := range []struct {
		name      string
		groupType querypb.Type
		charset   collations.ID
		old, new  sqltypes.Value
	}{
		{
			name: "case-insensitive text", groupType: querypb.Type_VARCHAR,
			charset: utf8mb40900AICI,
			old:     sqltypes.NewVarChar("a"), new: sqltypes.NewVarChar("A"),
		},
		{
			name: "equivalent decimals", groupType: querypb.Type_DECIMAL,
			charset: collations.CollationBinaryID,
			old:     sqltypes.NewDecimal("1.0"), new: sqltypes.NewDecimal("1.00"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldPart := dpcOrderingResult(tc.groupType, tc.charset,
				[]sqltypes.Value{tc.old})
			newPart := dpcOrderingResult(tc.groupType, tc.charset,
				[]sqltypes.Value{tc.new})
			oldPart.Rows[0][2] = sqltypes.NewInt64(1)
			newPart.Rows[0][2] = sqltypes.NewInt64(2)

			merged, err := dpcTestHandler.mergeResults(
				[]*sqltypes.Result{oldPart, newPart}, dpcOrderingPlan())
			if err != nil {
				t.Fatal(err)
			}
			if len(merged.Rows) != 1 {
				t.Fatalf("mergeResults returned %d rows, want 1", len(merged.Rows))
			}
			if got := merged.Rows[0][1].ToString(); got != tc.new.ToString() {
				t.Fatalf("representative group = %q, want newest %q", got, tc.new.ToString())
			}
			if got := merged.Rows[0][2].ToString(); got != "2" {
				t.Fatalf("representative value = %q, want newest value 2", got)
			}
		})
	}
}

func TestMergeKeepsMySQLDistinctGroups(t *testing.T) {
	oldPart := dpcOrderingResult(querypb.Type_VARBINARY, collations.CollationBinaryID,
		[]sqltypes.Value{sqltypes.NewVarBinary("a")})
	newPart := dpcOrderingResult(querypb.Type_VARBINARY, collations.CollationBinaryID,
		[]sqltypes.Value{sqltypes.NewVarBinary("A")})

	merged, err := dpcTestHandler.mergeResults(
		[]*sqltypes.Result{oldPart, newPart}, dpcOrderingPlan())
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rows) != 2 {
		t.Fatalf("mergeResults returned %d rows, want 2", len(merged.Rows))
	}
}

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
			_, err := dpcTestHandler.mergeResults([]*sqltypes.Result{input}, plan)
			if err == nil {
				t.Fatalf("mergeResults ordered an unorderable %v column", tc.groupType)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("mergeResults error = %v, want it to mention %q", err, tc.wantErr)
			}
			if _, err = dpcTestHandler.cropAndSortResult(input, plan,
				timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(1, 0)}); err == nil {
				t.Fatalf("cropAndSortResult ordered an unorderable %v column", tc.groupType)
			}
		})
	}
}

// TestUnmergeableDeltaPlanStopsRepeatingTheDeltaFetch asserts that a plan whose
// results can never be merged is recorded, so later requests degrade straight to
// the object cache instead of fetching every delta extent and discarding it.
func TestUnmergeableDeltaPlanStopsRepeatingTheDeltaFetch(t *testing.T) {
	origin, _, client := startLifecycleProxy(t, "mysql-dpc-fallback", time.Second,
		func(config *ProtocolConfig) {
			config.ProxyOnly = false
			config.Cache = newTestCache()
			config.CacheTTL = time.Hour
		})
	// The plan groups by an ENUM column, which DPC cannot order because the
	// result header does not carry the declaration values.
	origin.setResponder(func(string) *sqltypes.Result {
		return &sqltypes.Result{
			Fields: []*querypb.Field{
				{Name: "time", Type: querypb.Type_INT64},
				{Name: "tier", Type: querypb.Type_ENUM, Charset: uint32(utf8mb40900AICI)},
				{Name: "value", Type: querypb.Type_INT64},
			},
			Rows: [][]sqltypes.Value{
				{sqltypes.NewInt64(0),
					sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("small")),
					sqltypes.NewInt64(1)},
				{sqltypes.NewInt64(60),
					sqltypes.MakeTrusted(querypb.Type_ENUM, []byte("large")),
					sqltypes.NewInt64(2)},
			},
		}
	})
	const query = `SELECT
  cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time,
  tier AS tier,
  count(*) AS value
FROM events
WHERE ts >= FROM_UNIXTIME(0) AND ts < FROM_UNIXTIME(180)
GROUP BY time, tier
ORDER BY time, tier`

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

// TestMergeRejectsCollationChangeBetweenParts asserts parts whose group column
// changed collation are not merged, since every part would otherwise be ordered
// by the first part's collation.
func TestMergeRejectsCollationChangeBetweenParts(t *testing.T) {
	plan := dpcOrderingPlan()
	first := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI,
		[]sqltypes.Value{sqltypes.NewVarChar("a")})
	second := dpcOrderingResult(querypb.Type_VARCHAR, latin1SwedishCI,
		[]sqltypes.Value{sqltypes.NewVarChar("B")})
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{first, second}, plan); err == nil {
		t.Fatal("mergeResults accepted parts whose group collation changed")
	}
	// The same collation on both parts still merges.
	same := dpcOrderingResult(querypb.Type_VARCHAR, utf8mb40900AICI,
		[]sqltypes.Value{sqltypes.NewVarChar("B")})
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{first, same}, plan); err != nil {
		t.Fatalf("mergeResults rejected parts sharing one collation: %v", err)
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
	_, err := dpcTestHandler.mergeResults([]*sqltypes.Result{input}, plan)
	if err == nil {
		t.Fatal("mergeResults accepted a group value of the wrong type")
	}
	if !strings.Contains(err.Error(), "want VARCHAR") {
		t.Fatalf("mergeResults error = %v, want it to name the declared type", err)
	}
	if _, err = dpcTestHandler.cropAndSortResult(input, plan,
		timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(1, 0)}); err == nil {
		t.Fatal("cropAndSortResult accepted a group value of the wrong type")
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
		if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{input}, plan); err == nil {
			t.Fatal("mergeResults silently ordered rows under an unusable collation")
		}
		if _, err := dpcTestHandler.cropAndSortResult(input, plan,
			timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(1, 0)}); err == nil {
			t.Fatal("cropAndSortResult silently ordered rows under an unusable collation")
		}
	})

	t.Run("charset overflows collations.ID", func(t *testing.T) {
		// Truncating this to uint16 would wrap to Unknown and order under the
		// connection default instead of rejecting the column.
		input := dpcOrderingResult(querypb.Type_VARCHAR, 0, values)
		input.Fields[1].Charset = uint32(math.MaxUint16) + 1
		if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{input}, plan); err == nil {
			t.Fatal("mergeResults truncated an out-of-range collation to Unknown")
		}
		if _, err := dpcTestHandler.cropAndSortResult(input, plan,
			timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(1, 0)}); err == nil {
			t.Fatal("cropAndSortResult truncated an out-of-range collation to Unknown")
		}
	})
}
