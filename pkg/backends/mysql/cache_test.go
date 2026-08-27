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
	"fmt"
	"strings"
	"testing"
	"time"

	tcache "github.com/trickstercache/trickster/v2/pkg/cache"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtenv"
)

// dpcTestHandler supplies the collation environment DPC result ordering needs:
// group columns are compared with MySQL's own rules, which requires one.
var dpcTestHandler = &protocolHandler{env: vtenv.NewTestEnv()}

func TestCachedQueryResultRoundTrip(t *testing.T) {
	original := &cachedQueryResult{
		result: &sqltypes.Result{
			Fields:      []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
			Rows:        [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
			StatusFlags: 2,
		},
		extents: timeseries.ExtentList{{
			Start: time.Unix(60, 0), End: time.Unix(120, 0),
		}},
	}
	data, err := marshalCachedQueryResult(original)
	if err != nil {
		t.Fatal(err)
	}
	if data[5] != 0 || data[6] != 0 {
		t.Fatalf("reserved warning bytes were not zero: %v", data[5:7])
	}
	// Version-1 entries that populated the former warning field remain
	// readable; the unavailable value is deliberately ignored.
	data[5], data[6] = 0, 3
	got, err := unmarshalCachedQueryResult(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.result.StatusFlags != 2 ||
		len(got.result.Rows) != 1 || got.result.Rows[0][0].ToString() != "42" ||
		len(got.extents) != 1 || !got.extents[0].Start.Equal(time.Unix(60, 0)) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCachedQueryResultSizeEstimateTracksRetainedMemory(t *testing.T) {
	if estimateCachedQueryResultSize(nil) != 0 {
		t.Fatal("nil cached result had a size")
	}
	base := &cachedQueryResult{result: &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "value", Table: "events", OrgTable: "events",
			Database: "analytics", OrgName: "value", ColumnType: "varchar(255)"}},
		Rows:                [][]sqltypes.Value{{sqltypes.NewVarChar("payload")}},
		Info:                "info",
		SessionStateChanges: "state",
	}}
	baseSize := estimateCachedQueryResultSize(base)
	if baseSize <= len("payload") {
		t.Fatalf("estimated size = %d, want structural overhead included", baseSize)
	}

	rows := make([][]sqltypes.Value, 1, 16)
	rows[0] = make([]sqltypes.Value, 1, 8)
	rows[0][0] = sqltypes.NewVarChar("payload")
	extents := make(timeseries.ExtentList, 1, 8)
	retained := &cachedQueryResult{result: &sqltypes.Result{
		Fields: base.result.Fields, Rows: rows,
	}, extents: extents}
	if retainedSize := estimateCachedQueryResultSize(retained); retainedSize <= baseSize {
		t.Fatalf("retained-capacity size = %d, want greater than %d", retainedSize, baseSize)
	}
}

func TestByteCacheUsesEncodedSize(t *testing.T) {
	cacheClient := newTestCache()
	h := &protocolHandler{config: ProtocolConfig{Cache: cacheClient}}
	result := &cachedQueryResult{result: &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_VARCHAR}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewVarChar("payload")}},
	}}
	h.storeCached("key", result)
	if len(cacheClient.data["key"]) == 0 || result.Size() != len(cacheClient.data["key"]) {
		t.Fatalf("byte-cache size = %d/%d", result.Size(), len(cacheClient.data["key"]))
	}
}

func TestUnmarshalCachedQueryResultRejectsInvalidData(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not-a-cache-entry"),
		append(cacheEnvelopeMagic[:], cacheEnvelopeVersion)} {
		if _, err := unmarshalCachedQueryResult(data); err == nil {
			t.Fatalf("unmarshal accepted invalid data %q", data)
		}
	}
}

func TestMergeAndCropDeltaResults(t *testing.T) {
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "metric", Type: querypb.Type_VARCHAR},
		{Name: "value", Type: querypb.Type_INT64},
	}
	result := func(rows ...[]sqltypes.Value) *sqltypes.Result {
		return &sqltypes.Result{Fields: fields, Rows: rows}
	}
	parts := []*sqltypes.Result{
		result(
			[]sqltypes.Value{sqltypes.NewInt64(0), sqltypes.NewVarChar("b"), sqltypes.NewInt64(1)},
			[]sqltypes.Value{sqltypes.NewInt64(60), sqltypes.NewVarChar("a"), sqltypes.NewInt64(2)},
		),
		result(
			[]sqltypes.Value{sqltypes.NewInt64(0), sqltypes.NewVarChar("a"), sqltypes.NewInt64(3)},
			[]sqltypes.Value{sqltypes.NewInt64(60), sqltypes.NewVarChar("a"), sqltypes.NewInt64(4)},
			[]sqltypes.Value{sqltypes.NewInt64(120), sqltypes.NewVarChar("a"), sqltypes.NewInt64(5)},
		),
	}
	plan := &sqlanalyzer.QueryPlan{
		OutputColumn: "time", GroupColumns: []string{"metric"},
		OutputUnit: timeseries.DateTimeUnixSecs,
	}
	merged, err := dpcTestHandler.mergeResults(parts, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rows) != 4 || merged.Rows[0][1].ToString() != "a" ||
		merged.Rows[2][2].ToString() != "4" {
		t.Fatalf("unexpected merged rows: %+v", merged.Rows)
	}
	cropped, err := dpcTestHandler.cropAndSortResult(merged, plan,
		timeseries.Extent{Start: time.Unix(60, 0), End: time.Unix(120, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cropped.Rows) != 2 || cropped.Rows[0][0].ToString() != "60" ||
		cropped.Rows[1][0].ToString() != "120" {
		t.Fatalf("unexpected cropped rows: %+v", cropped.Rows)
	}
}

func TestMergeResultsPreservesNullAndEmptyGroupValues(t *testing.T) {
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "metric", Type: querypb.Type_VARCHAR},
		{Name: "value", Type: querypb.Type_INT64},
	}
	result := func(group sqltypes.Value, value int64) *sqltypes.Result {
		return &sqltypes.Result{Fields: fields, Rows: [][]sqltypes.Value{{
			sqltypes.NewInt64(0), group, sqltypes.NewInt64(value),
		}}}
	}
	plan := &sqlanalyzer.QueryPlan{
		OutputColumn: "time", GroupColumns: []string{"metric"},
		OutputUnit: timeseries.DateTimeUnixSecs,
	}
	merged, err := dpcTestHandler.mergeResults([]*sqltypes.Result{
		result(sqltypes.NULL, 1), result(sqltypes.NewVarChar(""), 2),
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rows) != 2 || !merged.Rows[0][1].IsNull() ||
		merged.Rows[1][1].IsNull() || merged.Rows[1][1].ToString() != "" {
		t.Fatalf("NULL and empty group values were not preserved: %+v", merged.Rows)
	}
}

func TestDeltaRetentionDoesNotTruncateCurrentResponse(t *testing.T) {
	const points = 6
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "value", Type: querypb.Type_INT64},
	}
	rows := make([][]sqltypes.Value, points)
	for i := range points {
		rows[i] = []sqltypes.Value{
			sqltypes.NewInt64(int64(i * 60)), sqltypes.NewInt64(int64(i)),
		}
	}
	merged := &sqltypes.Result{Fields: fields, Rows: rows}
	plan := &sqlanalyzer.QueryPlan{
		Step: time.Minute, OutputColumn: "time", OutputUnit: timeseries.DateTimeUnixSecs,
	}
	requested := timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(300, 0)}
	h := &protocolHandler{config: ProtocolConfig{RetentionPoints: 3}}

	response, retained, retainedExtents, err := h.finalizeDeltaResult(merged,
		timeseries.ExtentList{requested}, plan, requested, time.Unix(3600, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Rows) != points || response.Rows[0][0].ToString() != "0" ||
		response.Rows[points-1][0].ToString() != "300" {
		t.Fatalf("current response was retention-truncated: %+v", response.Rows)
	}
	if len(retained.Rows) != 3 || retained.Rows[0][0].ToString() != "180" ||
		retained.Rows[2][0].ToString() != "300" {
		t.Fatalf("cached result did not apply retention: %+v", retained.Rows)
	}
	if len(retainedExtents) != 1 || !retainedExtents[0].Start.Equal(time.Unix(180, 0)) ||
		!retainedExtents[0].End.Equal(time.Unix(300, 0)) {
		t.Fatalf("cached extents did not apply retention: %v", retainedExtents)
	}
}

func TestSortedDeltaFinalizationPreservesGroupedBoundaries(t *testing.T) {
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "metric", Type: querypb.Type_VARCHAR},
		{Name: "value", Type: querypb.Type_INT64},
	}
	rows := make([][]sqltypes.Value, 0, 12)
	for _, epoch := range []int64{0, 60, 120} {
		for _, metric := range []string{"a", "b"} {
			rows = append(rows, []sqltypes.Value{
				sqltypes.NewInt64(epoch), sqltypes.NewVarChar(metric), sqltypes.NewInt64(epoch),
			})
		}
	}
	merged := &sqltypes.Result{Fields: fields, Rows: rows}
	plan := &sqlanalyzer.QueryPlan{
		Step: time.Minute, OutputColumn: "time", GroupColumns: []string{"metric"},
		OutputUnit: timeseries.DateTimeUnixSecs,
	}
	h := &protocolHandler{config: ProtocolConfig{RetentionPoints: 2}}
	response, retained, extents, err := h.finalizeDeltaResult(merged,
		timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(120, 0)}},
		plan, timeseries.Extent{Start: time.Unix(60, 0), End: time.Unix(60, 0)},
		time.Unix(3600, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Rows) != 2 || response.Rows[0][0].ToString() != "60" ||
		response.Rows[1][1].ToString() != "b" {
		t.Fatalf("cropped grouped response = %+v", response.Rows)
	}
	if len(retained.Rows) != 4 || retained.Rows[0][0].ToString() != "60" ||
		retained.Rows[3][0].ToString() != "120" || &retained.Rows[0] == &merged.Rows[2] {
		t.Fatalf("retained grouped result = len/cap %d/%d, rows %+v",
			len(retained.Rows), cap(retained.Rows), retained.Rows)
	}
	if len(extents) != 1 || !extents[0].Start.Equal(time.Unix(60, 0)) {
		t.Fatalf("retained extents = %v", extents)
	}
}

func TestDPCLocksOnlySerializeMatchingKeys(t *testing.T) {
	left, right := collidingLockKeys()
	h := &protocolHandler{}
	leftLock := h.lockDPC(left)
	rightDone := make(chan struct{})
	go func() {
		lock := h.lockDPC(right)
		h.unlockDPC(right, lock)
		close(rightDone)
	}()
	select {
	case <-rightDone:
	case <-time.After(time.Second):
		h.unlockDPC(left, leftLock)
		t.Fatalf("distinct colliding keys %q and %q blocked each other", left, right)
	}

	sameDone := make(chan struct{})
	go func() {
		lock := h.lockDPC(left)
		h.unlockDPC(left, lock)
		close(sameDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		h.dpcLockMtx.Lock()
		references := leftLock.references
		h.dpcLockMtx.Unlock()
		if references == 2 {
			break
		}
		if time.Now().After(deadline) {
			h.unlockDPC(left, leftLock)
			t.Fatal("matching-key waiter did not register")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-sameDone:
		t.Fatal("matching keys did not serialize")
	default:
	}
	h.unlockDPC(left, leftLock)
	select {
	case <-sameDone:
	case <-time.After(time.Second):
		t.Fatal("matching-key waiter remained blocked")
	}
	h.dpcLockMtx.Lock()
	remaining := len(h.dpcLocks)
	h.dpcLockMtx.Unlock()
	if remaining != 0 {
		t.Fatalf("DPC lock registry retained %d entries", remaining)
	}
}

func collidingLockKeys() (string, string) {
	var keys [64]string
	for i := 0; ; i++ {
		key := fmt.Sprintf("key-%d", i)
		var hash uint64 = 14695981039346656037
		for j := range len(key) {
			hash ^= uint64(key[j])
			hash *= 1099511628211
		}
		bucket := hash % uint64(len(keys))
		if keys[bucket] != "" {
			return keys[bucket], key
		}
		keys[bucket] = key
	}
}

func TestStableExtentsExcludesBackfillWindow(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{BackfillPoints: 2}}
	plan := &sqlanalyzer.QueryPlan{Step: time.Minute}
	now := time.Unix(600, 0)
	extents := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(600, 0)}}
	got := h.stableExtents(extents, plan, now)
	if len(got) != 1 || !got[0].End.Equal(time.Unix(420, 0)) {
		t.Fatalf("stable extents = %v", got)
	}
}

func TestBuildDeltaRequestWindowNormalizesToCadence(t *testing.T) {
	plan := &sqlanalyzer.QueryPlan{
		Step:       time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: time.Unix(5, 0), Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: time.Unix(185, 0), Inclusive: false},
	}
	window, err := buildDeltaRequestWindow(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !window.output.Start.Equal(time.Unix(60, 0)) ||
		!window.output.End.Equal(time.Unix(120, 0)) {
		t.Fatalf("output extent = %v", window.output)
	}
	if len(window.cacheable) != 1 || !window.cacheable[0].Start.Equal(time.Unix(60, 0)) ||
		!window.cacheable[0].End.Equal(time.Unix(120, 0)) {
		t.Fatalf("cacheable extent = %v", window.cacheable)
	}
}

func TestBuildDeltaRequestWindowCollapsesRangeWithoutCompleteBucket(t *testing.T) {
	plan := &sqlanalyzer.QueryPlan{
		Step:       time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: time.Unix(5, 0), Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: time.Unix(25, 0), Inclusive: false},
	}
	window, err := buildDeltaRequestWindow(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !window.empty || !window.lower.Equal(time.Unix(60, 0)) ||
		!window.upper.Equal(time.Unix(60, 0)) || len(window.cacheable) != 0 {
		t.Fatalf("normalized empty window = %+v", window)
	}
}

func TestBuildDeltaRequestWindowPreservesAlignedLowerBound(t *testing.T) {
	plan := &sqlanalyzer.QueryPlan{
		Step:       time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: time.Unix(60, 0), Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: time.Unix(185, 0), Inclusive: false},
	}
	window, err := buildDeltaRequestWindow(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !window.lower.Equal(time.Unix(60, 0)) || !window.upper.Equal(time.Unix(180, 0)) {
		t.Fatalf("normalized aligned window = %+v", window)
	}
}

func TestBuildDeltaRequestWindowFiveMinuteExamples(t *testing.T) {
	tests := []struct {
		name              string
		lower, upper      int64
		wantLower, wantUp int64
		wantEmpty         bool
	}{
		{"aligned lower", 1785895200, 1785981892, 1785895200, 1785981600, false},
		{"unaligned range", 1785895492, 1785981892, 1785895500, 1785981600, false},
		{"equal bounds", 1785895492, 1785895492, 1785895500, 1785895500, true},
		{"one second range", 1785895492, 1785895493, 1785895500, 1785895500, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window, err := buildDeltaRequestWindow(&sqlanalyzer.QueryPlan{
				Step:       5 * time.Minute,
				LowerBound: &sqlanalyzer.Bound{Value: time.Unix(test.lower, 0), Inclusive: true},
				UpperBound: &sqlanalyzer.Bound{Value: time.Unix(test.upper, 0), Inclusive: false},
			})
			if err != nil {
				t.Fatal(err)
			}
			if window.lower.Unix() != test.wantLower || window.upper.Unix() != test.wantUp ||
				window.empty != test.wantEmpty {
				t.Fatalf("normalized window = [%d, %d), empty=%t; want [%d, %d), empty=%t",
					window.lower.Unix(), window.upper.Unix(), window.empty,
					test.wantLower, test.wantUp, test.wantEmpty)
			}
		})
	}
}

func TestDeltaCoveragePlansExpectedOriginSQLAndMergedResult(t *testing.T) {
	analysis := mustNewAnalyzer().Analyze(strings.ReplaceAll(safeDateTimeQuery,
		"300", "60"), time.Time{})
	if analysis.Plan == nil {
		t.Fatalf("Analyze(): %v", analysis.Err)
	}
	plan := analysis.Plan
	need := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(300, 0)}}
	tests := []struct {
		name    string
		covered timeseries.ExtentList
		want    timeseries.ExtentList
	}{
		{"full miss", nil, need},
		{"hit", need, nil},
		{"partial hit", timeseries.ExtentList{
			{Start: time.Unix(0, 0), End: time.Unix(60, 0)},
			{Start: time.Unix(180, 0), End: time.Unix(300, 0)},
		}, timeseries.ExtentList{{Start: time.Unix(120, 0), End: time.Unix(120, 0)}}},
		{"range miss", timeseries.ExtentList{{
			Start: time.Unix(600, 0), End: time.Unix(660, 0),
		}}, need},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.covered.CalculateDeltas(need.Clone(), plan.Step)
			if !extentListsEqual(got, tc.want) {
				t.Fatalf("missing extents = %v, want %v", got, tc.want)
			}
		})
	}

	shards := need.Splice(plan.Step, 0, 0, 2)
	if len(shards) != 3 {
		t.Fatalf("sharded miss = %v, want three two-point extents", shards)
	}
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "trips", Type: querypb.Type_INT64},
	}
	parts := make([]*sqltypes.Result, 0, len(shards))
	for _, shard := range shards {
		rendered, err := plan.RenderExtent(shard)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rendered, fmt.Sprintf("FROM_UNIXTIME(%d)", shard.Start.Unix())) ||
			!strings.Contains(rendered, fmt.Sprintf("FROM_UNIXTIME(%d)",
				shard.End.Add(plan.Step).Unix())) {
			t.Fatalf("origin SQL does not match shard %v: %s", shard, rendered)
		}
		rows := make([][]sqltypes.Value, 0, 2)
		for at := shard.Start; !at.After(shard.End); at = at.Add(plan.Step) {
			rows = append(rows, []sqltypes.Value{
				sqltypes.NewInt64(at.Unix()), sqltypes.NewInt64(at.Unix() / 60),
			})
		}
		parts = append(parts, &sqltypes.Result{Fields: fields, Rows: rows})
	}
	merged, err := dpcTestHandler.mergeResults(parts, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rows) != 6 || merged.Rows[0][0].ToString() != "0" ||
		merged.Rows[5][0].ToString() != "300" {
		t.Fatalf("merged shard result = %+v", merged.Rows)
	}
}

func extentListsEqual(left, right timeseries.ExtentList) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !left[i].Start.Equal(right[i].Start) || !left[i].End.Equal(right[i].End) {
			return false
		}
	}
	return true
}

func TestCacheIdentityIsolationAndCanonicalization(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{
		BackendName: "mysql-identity", CacheKeyPrefix: "shared",
	}}
	key := func(tb testing.TB, query, user, database, timeZone string) string {
		tb.Helper()
		analysis := mustNewAnalyzer().Analyze(query, time.Time{})
		session := &upstreamSession{database: database, timeZone: timeZone}
		connection := &vtmysql.Conn{User: user}
		if analysis.Plan != nil {
			return h.queryCacheKey(connection, session, "dpc",
				analysis.Plan.CanonicalSQL, analysis.Plan.IdentitySuffix)
		}
		if analysis.Mode != sqlanalyzer.CacheModeObject {
			tb.Fatalf("Analyze(%q): mode=%s reason=%s err=%v", query,
				analysis.Mode, analysis.Reason, analysis.Err)
		}
		return h.queryCacheKey(connection, session, "opc", strings.TrimSpace(query))
	}
	base := key(t, safeDateTimeQuery, "alice", "analytics", "+00:00")
	otherExtent := strings.NewReplacer(
		"1785542400", "1785628800", "1785628800", "1785715200",
	).Replace(safeDateTimeQuery)
	if got := key(t, otherExtent, "alice", "analytics", "+00:00"); got != base {
		t.Fatalf("time extents use different DPC keys: %q != %q", got, base)
	}
	for _, tc := range []struct {
		name, user, database, timeZone string
	}{
		{name: "username", user: "bob", database: "analytics", timeZone: "+00:00"},
		{name: "database", user: "alice", database: "reporting", timeZone: "+00:00"},
		{name: "time zone", user: "alice", database: "analytics", timeZone: "-07:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := key(t, safeDateTimeQuery, tc.user, tc.database, tc.timeZone); got == base {
				t.Fatalf("%s shared the base DPC identity", tc.name)
			}
			opcBase := h.queryCacheKey(&vtmysql.Conn{User: "alice"},
				&upstreamSession{database: "analytics", timeZone: "+00:00"}, "opc", "SELECT 42")
			opcGot := h.queryCacheKey(&vtmysql.Conn{User: tc.user},
				&upstreamSession{database: tc.database, timeZone: tc.timeZone}, "opc", "SELECT 42")
			if opcGot == opcBase {
				t.Fatalf("%s shared the base OPC identity", tc.name)
			}
		})
	}

	formattingOnly := strings.Replace(safeDateTimeQuery, "SELECT\n  ", "SELECT     ", 1)
	if got := key(t, formattingOnly, "alice", "analytics", "+00:00"); got != base {
		t.Fatalf("formatting-only DPC variation did not converge: %q != %q", got, base)
	}
	for name, query := range map[string]string{
		"predicate": strings.Replace(safeDateTimeQuery, "WHERE ",
			"WHERE cab_type = 'yellow' AND ", 1),
		"selected column": strings.Replace(safeDateTimeQuery, "count(*) AS trips",
			"sum(passenger_count) AS trips", 1),
		"grouping": strings.Replace(strings.Replace(safeDateTimeQuery,
			"count(*) AS trips", "cab_type AS metric, count(*) AS trips", 1),
			"GROUP BY time", "GROUP BY time, metric", 1),
		"ordering": strings.Replace(safeDateTimeQuery, "ORDER BY time", "ORDER BY time, trips", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got := key(t, query, "alice", "analytics", "+00:00"); got == base {
				t.Fatalf("%s change shared the base identity", name)
			}
		})
	}

	opcCompact := h.queryCacheKey(&vtmysql.Conn{User: "alice"},
		&upstreamSession{database: "analytics"}, "opc", "SELECT count(*) FROM trips")
	opcFormatted := h.queryCacheKey(&vtmysql.Conn{User: "alice"},
		&upstreamSession{database: "analytics"}, "opc", "SELECT  count(*) FROM trips")
	if opcCompact == opcFormatted {
		t.Fatal("undocumented OPC formatting normalization changed cache identity")
	}
}

func TestCharacterSetAndCollationStateBypassesCache(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{Cache: newTestCache()}}
	for _, query := range []string{
		"SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		"SET character_set_results = utf8mb4",
		"SET collation_connection = utf8mb4_0900_ai_ci",
	} {
		t.Run(query, func(t *testing.T) {
			session := &upstreamSession{}
			h.updateSessionStateParsed(session, parseQuery(query))
			if h.cacheEligible(session) {
				t.Fatalf("%q remained cache-eligible", query)
			}
		})
	}
}

func TestObserveCacheRecordsEveryNativeAndStandardOutcomeOnce(t *testing.T) {
	tests := []struct {
		mode     sqlanalyzer.CacheMode
		statuses []cachestatus.LookupStatus
	}{
		{mode: sqlanalyzer.CacheModeObject, statuses: []cachestatus.LookupStatus{
			cachestatus.LookupStatusKeyMiss, cachestatus.LookupStatusHit,
			cachestatus.LookupStatusProxyOnly, cachestatus.LookupStatusProxyError,
		}},
		{mode: sqlanalyzer.CacheModeDelta, statuses: []cachestatus.LookupStatus{
			cachestatus.LookupStatusKeyMiss, cachestatus.LookupStatusRangeMiss,
			cachestatus.LookupStatusPartialHit, cachestatus.LookupStatusHit,
			cachestatus.LookupStatusProxyOnly, cachestatus.LookupStatusProxyError,
		}},
	}
	for _, tc := range tests {
		for _, cacheStatus := range tc.statuses {
			name := tc.mode.String() + "/" + cacheStatus.String()
			t.Run(name, func(t *testing.T) {
				backendName := "mysql-cache-metrics-" + tc.mode.String() + "-" + cacheStatus.String()
				h := &protocolHandler{config: ProtocolConfig{BackendName: backendName}}
				httpStatus := "200"
				if cacheStatus == cachestatus.LookupStatusProxyError {
					httpStatus = "500"
				}
				native := metrics.SQLQueryCache.WithLabelValues(backendName, mysqlDialect,
					tc.mode.String(), cacheStatus.String())
				request := metrics.ProxyRequestStatus.WithLabelValues(backendName, mysqlDialect,
					"QUERY", cacheStatus.String(), httpStatus, "query")
				elements := metrics.ProxyRequestElements.WithLabelValues(backendName, mysqlDialect,
					cacheStatus.String(), "query")
				duration := metrics.ProxyRequestDuration.WithLabelValues(backendName, mysqlDialect,
					"QUERY", cacheStatus.String(), httpStatus, "query")
				nativeBefore, requestBefore := testutil.ToFloat64(native), testutil.ToFloat64(request)
				elementsBefore := testutil.ToFloat64(elements)
				durationBefore := histogramSampleCount(t, duration)

				h.observeCache(tc.mode, cacheStatus, 7, time.Millisecond)
				if got := testutil.ToFloat64(native); got != nativeBefore+1 {
					t.Fatalf("native counter = %v, want %v", got, nativeBefore+1)
				}
				if got := testutil.ToFloat64(request); got != requestBefore+1 {
					t.Fatalf("request counter = %v, want %v", got, requestBefore+1)
				}
				if got := testutil.ToFloat64(elements); got != elementsBefore+7 {
					t.Fatalf("element counter = %v, want %v", got, elementsBefore+7)
				}
				if got := histogramSampleCount(t, duration); got != durationBefore+1 {
					t.Fatalf("duration samples = %d, want %d", got, durationBefore+1)
				}
			})
		}
	}
}

func histogramSampleCount(t *testing.T, observer any) uint64 {
	t.Helper()
	metric, ok := observer.(interface{ Write(*dto.Metric) error })
	if !ok {
		t.Fatalf("observer %T is not a Prometheus metric", observer)
	}
	value := &dto.Metric{}
	if err := metric.Write(value); err != nil {
		t.Fatal(err)
	}
	return value.GetHistogram().GetSampleCount()
}

func TestCacheFailureMetricsAndMaximumObjectRejection(t *testing.T) {
	result := &cachedQueryResult{result: &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_VARCHAR}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewVarChar("not small")}},
	}}
	for _, tc := range []struct {
		name, reason string
		configure    func(*testCache, *protocolHandler)
		run          func(*testCache, *protocolHandler)
	}{
		{
			name: "store failure", reason: "mysql_store_failure",
			configure: func(cache *testCache, _ *protocolHandler) {
				cache.storeErr = errors.New("store failed")
			},
			run: func(_ *testCache, h *protocolHandler) { h.storeCached("key", result) },
		},
		{
			name: "decode failure", reason: "mysql_decode_failure",
			configure: func(cache *testCache, _ *protocolHandler) {
				cache.data["key"] = []byte("corrupt")
			},
			run: func(_ *testCache, h *protocolHandler) { _, _ = h.retrieveCached("key") },
		},
		{
			name: "maximum object size", reason: "mysql_max_object_size",
			configure: func(_ *testCache, h *protocolHandler) { h.config.MaxObjectSize = 1 },
			run:       func(_ *testCache, h *protocolHandler) { h.storeCached("key", result) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuration := cacheoptions.New()
			configuration.Name = "mysql-failure-" + strings.ReplaceAll(tc.name, " ", "-")
			configuration.Provider = "memory"
			cache := newTestCache()
			cache.configuration = configuration
			h := &protocolHandler{config: ProtocolConfig{Cache: cache}}
			event := metrics.CacheEvents.WithLabelValues(configuration.Name,
				configuration.Provider, "error", tc.reason)
			before := testutil.ToFloat64(event)
			tc.configure(cache, h)
			tc.run(cache, h)
			if got := testutil.ToFloat64(event); got != before+1 {
				t.Fatalf("cache failure event = %v, want %v", got, before+1)
			}
			cache.mtx.Lock()
			_, stored := cache.data["key"]
			cache.mtx.Unlock()
			if tc.name == "maximum object size" && stored {
				t.Fatal("oversized cache object was stored")
			}
			if tc.name == "decode failure" && stored {
				t.Fatal("corrupt cache object was not removed")
			}
		})
	}
}

func TestSharedCacheOperationsUseCacheNameNotBackendName(t *testing.T) {
	configuration := cacheoptions.New()
	configuration.Name = "mysql-shared-cache"
	configuration.Provider = "memory"
	shared := cachemanager.NewCache(newTestCache(), cachemanager.CacheOptions{}, configuration)
	set := metrics.CacheObjectOperations.WithLabelValues(configuration.Name,
		configuration.Provider, "set", "none")
	before := testutil.ToFloat64(set)
	result := &cachedQueryResult{result: &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
	}}
	for _, backend := range []string{"mysql-a", "mysql-b"} {
		h := &protocolHandler{config: ProtocolConfig{BackendName: backend, Cache: shared}}
		h.storeCached(backend, result)
	}
	if got := testutil.ToFloat64(set); got != before+2 {
		t.Fatalf("shared-cache stores = %v, want %v", got, before+2)
	}
}

func TestMemoryProviderRetainsTypedCacheResult(t *testing.T) {
	configuration := cacheoptions.New()
	configuration.Name = "mysql-reference-cache"
	configuration.Provider = cacheproviders.Memory
	memoryClient := cachememory.New(configuration.Name, configuration)
	cacheClient := cachemanager.NewCache(memoryClient, cachemanager.CacheOptions{}, configuration)
	if err := cacheClient.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cacheClient.Close() })
	h := &protocolHandler{config: ProtocolConfig{Cache: cacheClient}}
	original := &cachedQueryResult{result: &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
	}}
	h.storeCached("key", original)
	got, found := h.retrieveCached("key")
	wantSize := estimateCachedQueryResultSize(original)
	if !found || got != original || got.Size() != wantSize {
		t.Fatalf("typed memory result = %p/%d, want %p/%d", got, got.Size(), original, wantSize)
	}
	rejected := &cachedQueryResult{result: original.result}
	h.config.MaxObjectSize = int64(estimateCachedQueryResultSize(rejected) - 1)
	h.storeCached("rejected", rejected)
	if _, _, err := memoryClient.RetrieveReference("rejected"); !errors.Is(err, tcache.ErrKNF) {
		t.Fatalf("oversized reference was stored: %v", err)
	}
	h.config.MaxObjectSize = 0

	invalid := &cachedQueryResult{size: 10}
	if err := memoryClient.StoreReference("invalid", invalid, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found := h.retrieveCached("invalid"); found {
		t.Fatal("typed memory cache returned a nil result")
	}
	if _, _, err := memoryClient.RetrieveReference("invalid"); !errors.Is(err, tcache.ErrKNF) {
		t.Fatalf("invalid typed result was not removed: %v", err)
	}

	oversized := &cachedQueryResult{result: original.result, size: 10}
	if err := memoryClient.StoreReference("oversized", oversized, time.Minute); err != nil {
		t.Fatal(err)
	}
	h.config.MaxObjectSize = 1
	if _, found := h.retrieveCached("oversized"); found {
		t.Fatal("oversized typed memory result returned a hit")
	}
}

func TestRewriteFailureMetricIsBoundedAndAttributed(t *testing.T) {
	const backendName = "mysql-rewrite-failure"
	h := &protocolHandler{config: ProtocolConfig{BackendName: backendName}}
	rewrites := metrics.SQLQueryRewriteFailures.WithLabelValues(backendName,
		mysqlDialect, "render_extent")
	before := testutil.ToFloat64(rewrites)
	plan := &sqlanalyzer.QueryPlan{Renderer: failingExtentRenderer{}}
	if _, err := plan.RenderExtent(timeseries.Extent{}); err == nil {
		t.Fatal("failing renderer returned no error")
	}
	h.observeRewriteFailure("render_extent")
	if got := testutil.ToFloat64(rewrites); got != before+1 {
		t.Fatalf("rewrite failures = %v, want %v", got, before+1)
	}
}

type failingExtentRenderer struct{}

func (failingExtentRenderer) RenderExtent(timeseries.Extent) (string, error) {
	return "", errors.New("render failed")
}

func BenchmarkCachedQueryResultCodec(b *testing.B) {
	rows := make([][]sqltypes.Value, 1000)
	for i := range rows {
		rows[i] = []sqltypes.Value{sqltypes.NewInt64(int64(i * 60)), sqltypes.NewInt64(int64(i))}
	}
	cached := &cachedQueryResult{
		result: &sqltypes.Result{
			Fields: []*querypb.Field{
				{Name: "time", Type: querypb.Type_INT64},
				{Name: "value", Type: querypb.Type_INT64},
			},
			Rows: rows,
		},
		extents: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(59_940, 0)}},
	}
	b.ReportAllocs()
	for b.Loop() {
		data, err := marshalCachedQueryResult(cached)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := unmarshalCachedQueryResult(data); err != nil {
			b.Fatal(err)
		}
	}
}
