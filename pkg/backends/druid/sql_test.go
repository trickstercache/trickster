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

package druid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const druidSQLTestStatement = `SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, SUM(v) AS value, host FROM foo WHERE __time >= TIMESTAMP '2024-01-01 00:00:00' AND __time < TIMESTAMP '2024-01-02 00:00:00' GROUP BY 1, host`

func druidSQLTestRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2/sql", strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	return r
}

func TestParseDruidSQLQuery(t *testing.T) {
	body := `{"resultFormat":"object","context":{"queryId":"transient","sqlTimeZone":"UTC"},"query":` + strconvQuote(druidSQLTestStatement) + `}`
	r := druidSQLTestRequest(body)
	trq, ro, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil || !canOPC {
		t.Fatalf("parse = trq=%v options=%v canOPC=%t err=%v", trq, ro, canOPC, err)
	}
	marker, ok := trq.ParsedQuery.(*model.SQLQueryPlan)
	if !ok || marker.Plan == nil {
		t.Fatalf("parsed query = %#v", trq.ParsedQuery)
	}
	if trq.Step != time.Hour || trq.Extent.Start.UTC().Format(time.RFC3339) != "2024-01-01T00:00:00Z" ||
		trq.Extent.End.UTC().Format(time.RFC3339) != "2024-01-01T23:00:00Z" {
		t.Fatalf("unexpected cadence/extent: step=%s extent=%s", trq.Step, trq.Extent)
	}
	if ro.ProviderRequest != marker || !ro.FastForwardDisable || ro.BaseTimestampFieldName != "__time" {
		t.Fatalf("unexpected options: %#v", ro)
	}
	if string(trq.OriginalBody) != body || !strings.Contains(trq.Statement, "<$TS1$>") ||
		!strings.Contains(trq.Statement, "<$TS2$>") {
		t.Fatalf("canonical/original body mismatch: statement=%s original=%s", trq.Statement, trq.OriginalBody)
	}
	canonicalBody, err := request.GetBody(r)
	if err != nil || !bytes.Contains(canonicalBody, []byte("$TS1$")) || bytes.Equal(canonicalBody, []byte(body)) {
		t.Fatalf("request body was not canonicalized: %s", canonicalBody)
	}

	var canonical map[string]any
	if err := json.Unmarshal(canonicalBody, &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical["context"].(map[string]any)["queryId"] != nil {
		t.Fatal("transport-only queryId leaked into canonical body")
	}

	rewrite := druidSQLTestRequest(body)
	trq2, _, _, err := (&Client{}).ParseTimeRangeQuery(rewrite)
	if err != nil {
		t.Fatal(err)
	}
	extent := trq2.Extent
	if err := (&Client{}).SetExtent(rewrite, trq2, &extent); err != nil {
		t.Fatal(err)
	}
	rewritten, _ := request.GetBody(rewrite)
	if !bytes.Contains(rewritten, []byte("2024-01-02 00:00:00")) ||
		!bytes.Contains(rewritten, []byte(`"queryId":"transient"`)) {
		t.Fatalf("unexpected rewritten body: %s", rewritten)
	}
}

func TestParseDruidSQLArrayHeaderQuery(t *testing.T) {
	body := `{"resultFormat":"array","header":true,"context":{"sqlTimeZone":"UTC"},"query":` + strconvQuote(druidSQLTestStatement) + `}`
	r := druidSQLTestRequest(body)
	trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil || !canOPC {
		t.Fatalf("parse = trq=%v canOPC=%t err=%v", trq, canOPC, err)
	}
	marker, ok := trq.ParsedQuery.(*model.SQLQueryPlan)
	if !ok || marker.Plan == nil {
		t.Fatalf("parsed query = %#v", trq.ParsedQuery)
	}
	if marker.ResponseFormat() != model.SQLResponseArray || !marker.Header() {
		t.Fatalf("response shape = format %d header %t", marker.ResponseFormat(), marker.Header())
	}
	if !slices.Equal(marker.OutputColumns(), []string{"bucket", "value", "host"}) ||
		!slices.Equal(marker.Plan.ValueColumns, []string{"value"}) {
		t.Fatalf("output columns = %v, values = %v",
			marker.OutputColumns(), marker.Plan.ValueColumns)
	}
	canonicalBody, err := request.GetBody(r)
	if err != nil || !bytes.Contains(canonicalBody, []byte(`"resultFormat":"array"`)) {
		t.Fatalf("canonical request body: %s (%v)", canonicalBody, err)
	}
}

func TestParseDruidSQLGrafanaMillisBounds(t *testing.T) {
	statement := `SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, COUNT(*) AS trips ` +
		`FROM "trips" WHERE __time >= MILLIS_TO_TIMESTAMP(1704067200123) ` +
		`AND __time < MILLIS_TO_TIMESTAMP(1704078000456) GROUP BY 1 ORDER BY 1`
	body := `{"query":` + strconvQuote(statement) +
		`,"resultFormat":"array","header":true,"context":{"sqlTimeZone":"UTC"}}`
	r := druidSQLTestRequest(body)
	trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(r)
	if err != nil || !canOPC {
		t.Fatalf("parse = trq=%v canOPC=%t err=%v", trq, canOPC, err)
	}
	marker, ok := trq.ParsedQuery.(*model.SQLQueryPlan)
	if !ok || marker.Plan == nil {
		t.Fatalf("parsed query = %#v", trq.ParsedQuery)
	}
	if got, want := trq.Extent.Start.UTC().Format(time.RFC3339Nano),
		"2024-01-01T01:00:00Z"; got != want {
		t.Fatalf("extent start = %s, want %s", got, want)
	}
	if got, want := trq.Extent.End.UTC().Format(time.RFC3339Nano),
		"2024-01-01T02:00:00Z"; got != want {
		t.Fatalf("extent end = %s, want %s", got, want)
	}
	extent := trq.Extent
	if err := (&Client{}).SetExtent(r, trq, &extent); err != nil {
		t.Fatal(err)
	}
	rewritten, err := request.GetBody(r)
	if err != nil || bytes.Contains(rewritten, []byte("MILLIS_TO_TIMESTAMP")) ||
		!bytes.Contains(rewritten, []byte("TIMESTAMP")) {
		t.Fatalf("rewritten Grafana SQL = %s (%v)", rewritten, err)
	}
}

func TestParseDruidSQLFallbacks(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"array result", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"resultFormat":"array"}`},
		{"array without header", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"resultFormat":"array","header":false}`},
		{"header result", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"header":true}`},
		{"non-UTC context", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"context":{"sqlTimeZone":"America/Los_Angeles"}}`},
		{"millisecond timestamps", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"context":{"serializeDateTimeAsLong":true}}`},
		{"inner millisecond timestamps", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"context":{"serializeDateTimeAsLongInner":true}}`},
		{"invalid timestamp serialization context", `{"query":` + strconvQuote(druidSQLTestStatement) + `,"context":{"serializeDateTimeAsLong":"true"}}`},
		{"calendar bucket", `{"query":` + strconvQuote(strings.Replace(druidSQLTestStatement, "'PT1H'", "'P1M'", 1)) + `}`},
		{"no bucket", `{"query":"SELECT COUNT(*) AS value FROM foo WHERE __time >= TIMESTAMP '2024-01-01 00:00:00'"}`},
		{"computed millis bound", `{"query":"SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, COUNT(*) AS value FROM foo WHERE __time >= MILLIS_TO_TIMESTAMP(epoch_ms) AND __time < MILLIS_TO_TIMESTAMP(1704153600000) GROUP BY 1"}`},
		{"unaliased value", `{"query":"SELECT TIME_FLOOR(__time, 'PT1H') AS bucket, COUNT(*) FROM foo WHERE __time >= TIMESTAMP '2024-01-01 00:00:00' AND __time < TIMESTAMP '2024-01-02 00:00:00' GROUP BY 1"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq, _, canOPC, err := (&Client{}).ParseTimeRangeQuery(druidSQLTestRequest(test.body))
			if err == nil || !canOPC || trq == nil {
				t.Fatalf("got trq=%v canOPC=%t err=%v", trq, canOPC, err)
			}
		})
	}
}

// druidMillisBucket renders the millisecond-arithmetic bucket a dynamic
// Grafana interval produces, around a range wide enough to hold whole buckets.
func druidMillisBucket(selectExpr string) string {
	return "SELECT " + selectExpr + ` AS bucket, COUNT(*) AS trips FROM "trips" ` +
		`WHERE __time >= MILLIS_TO_TIMESTAMP(1788000000000) ` +
		`AND __time < MILLIS_TO_TIMESTAMP(1789086400000) GROUP BY 1 ORDER BY 1`
}

// TestDruidMillisBucketIntervals covers every interval a dashboard can request.
// Druid's TIME_FLOOR takes a literal ISO period, so a dynamic interval reaches
// SQL only as a millisecond count.
func TestDruidMillisBucketIntervals(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		millis int64
		want   time.Duration
	}{
		{1000, time.Second},
		{10000, 10 * time.Second},
		{60000, time.Minute},
		{300000, 5 * time.Minute},
		{900000, 15 * time.Minute},
		{3600000, time.Hour},
		{43200000, 12 * time.Hour},
		{86400000, 24 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.want.String(), func(t *testing.T) {
			statement := druidMillisBucket(fmt.Sprintf(
				"MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / %d) * %d AS BIGINT))",
				test.millis, test.millis))
			analysis := druidSQLAnalyzer.Analyze(normalizeDruidSQL(statement), now)
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
				t.Fatalf("Analyze() = %s/%s (%v), want delta", analysis.Mode,
					analysis.Reason, analysis.Err)
			}
			if analysis.Plan.Step != test.want {
				t.Errorf("step = %v, want %v", analysis.Plan.Step, test.want)
			}
			if !druidSQLPlanSupported(analysis.Plan) {
				t.Error("plan shape rejected")
			}
		})
	}
}

// TestDruidMillisBucketForms verifies the spellings of the same bucket, since
// the FLOOR and the CAST are redundant over Druid's integer division.
func TestDruidMillisBucketForms(t *testing.T) {
	now := time.Now().UTC()
	accepted := map[string]string{
		"grafana":         `MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / 300000) * 300000 AS BIGINT))`,
		"no cast":         `MILLIS_TO_TIMESTAMP(FLOOR(TIMESTAMP_TO_MILLIS(__time) / 300000) * 300000)`,
		"no floor":        `MILLIS_TO_TIMESTAMP(TIMESTAMP_TO_MILLIS(__time) / 300000 * 300000)`,
		"multiplier left": `MILLIS_TO_TIMESTAMP(300000 * FLOOR(TIMESTAMP_TO_MILLIS(__time) / 300000))`,
		"extra parens":    `MILLIS_TO_TIMESTAMP(((FLOOR((TIMESTAMP_TO_MILLIS(__time)) / (300000))) * (300000)))`,
		"lowercase":       `millis_to_timestamp(cast(floor(timestamp_to_millis(__time) / 300000) * 300000 as bigint))`,
		"time_floor":      `TIME_FLOOR(__time, 'PT5M')`,
	}
	for name, selectExpr := range accepted {
		t.Run(name, func(t *testing.T) {
			analysis := druidSQLAnalyzer.Analyze(
				normalizeDruidSQL(druidMillisBucket(selectExpr)), now)
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
				t.Fatalf("Analyze() = %s/%s (%v), want delta", analysis.Mode,
					analysis.Reason, analysis.Err)
			}
			if analysis.Plan.Step != 5*time.Minute {
				t.Errorf("step = %v, want 5m", analysis.Plan.Step)
			}
		})
	}

	rejected := map[string]string{
		// truncating to one width and scaling by another is not a bucket
		"mismatched factors": `MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / 300000) * 60000 AS BIGINT))`,
		"zero divisor":       `MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / 0) * 0 AS BIGINT))`,
		"negative divisor":   `MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / -300000) * -300000 AS BIGINT))`,
		"other column":       `MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(dropoff_time) / 300000) * 300000 AS BIGINT))`,
		"not a bucket":       `MILLIS_TO_TIMESTAMP(TIMESTAMP_TO_MILLIS(__time))`,
	}
	for name, selectExpr := range rejected {
		t.Run("rejected/"+name, func(t *testing.T) {
			analysis := druidSQLAnalyzer.Analyze(
				normalizeDruidSQL(druidMillisBucket(selectExpr)), now)
			if analysis.Mode == sqlanalyzer.CacheModeDelta {
				t.Fatalf("Analyze() = delta with step %v, want object", analysis.Plan.Step)
			}
			if analysis.Reason != sqlanalyzer.ReasonUnsupportedBucket {
				t.Errorf("reason = %s, want unsupported_bucket", analysis.Reason)
			}
		})
	}
}

// TestDruidMillisBucketRendersWithoutCast pins the regression that made a
// dashboard's dynamic-interval panel fail: the canonical statement is
// re-rendered by the shared parser, which writes CAST(... AS BIGINT) with its
// own type name, and Druid rejects that identifier.
func TestDruidMillisBucketRendersWithoutCast(t *testing.T) {
	now := time.Now().UTC()
	statement := druidMillisBucket(
		`MILLIS_TO_TIMESTAMP(CAST(FLOOR(TIMESTAMP_TO_MILLIS(__time) / 60000) * 60000 AS BIGINT))`)
	normalized := normalizeDruidSQL(statement)
	analysis := druidSQLAnalyzer.Analyze(normalized, now)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("Analyze() = %s/%s (%v), want delta", analysis.Mode,
			analysis.Reason, analysis.Err)
	}
	extent := analysis.Plan.RequestExtent(now)
	rendered, err := analysis.Plan.RenderExtent(extent)
	if err != nil {
		t.Fatalf("RenderExtent() error = %v", err)
	}
	for _, statement := range []string{analysis.Plan.CanonicalSQL, rendered} {
		if strings.Contains(strings.ToUpper(statement), "CAST") {
			t.Errorf("statement retains a cast Druid cannot parse: %s", statement)
		}
		if strings.Contains(strings.ToUpper(statement), "INT8") {
			t.Errorf("statement uses the parser's own type name: %s", statement)
		}
	}
	if !strings.Contains(rendered, "millis_to_timestamp") {
		t.Errorf("rendered statement lost its bucket: %s", rendered)
	}
}

// TestDruidMillisBucketCastFailsClosed verifies a cast the normalizer cannot
// remove is rejected rather than rendered into SQL the origin refuses.
func TestDruidMillisBucketCastFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	statement := druidMillisBucket(
		`MILLIS_TO_TIMESTAMP(FLOOR(CAST(TIMESTAMP_TO_MILLIS(__time) AS BIGINT) / 300000) * 300000)`)
	analysis := druidSQLAnalyzer.Analyze(normalizeDruidSQL(statement), now)
	if analysis.Mode == sqlanalyzer.CacheModeDelta {
		t.Fatalf("Analyze() = delta, want object for an unremovable cast")
	}
	if analysis.Reason != sqlanalyzer.ReasonUnsupportedBucket {
		t.Errorf("reason = %s, want unsupported_bucket", analysis.Reason)
	}
}

// TestDruidSQLRenderable covers the constructs the shared parser reformats
// into spellings Druid's planner rejects. Each rejected case was confirmed
// against a live Druid: the original parses and the reformatted form does not.
func TestDruidSQLRenderable(t *testing.T) {
	unrenderable := map[string]string{
		"bigint cast":  `SELECT CAST(x AS INT8) FROM trips`,
		"float cast":   `SELECT CAST(x AS FLOAT8) FROM trips`,
		"boolean cast": `SELECT CAST(x AS BOOL) FROM trips`,
		"extract":      `SELECT extract('hour', __time) FROM trips`,
		"pg cast":      `SELECT '1'::INTERVAL HOUR FROM trips`,
	}
	for name, canonical := range unrenderable {
		t.Run(name, func(t *testing.T) {
			if druidSQLRenderable(canonical) {
				t.Errorf("statement reported renderable: %s", canonical)
			}
		})
	}

	renderable := map[string]string{
		"double cast":    `SELECT CAST(x AS double) FROM trips`,
		"varchar cast":   `SELECT CAST(x AS VARCHAR) FROM trips`,
		"decimal cast":   `SELECT CAST(x AS DECIMAL) FROM trips`,
		"timestamp cast": `SELECT CAST(x AS TIMESTAMP) FROM trips`,
		"count distinct": `SELECT count(DISTINCT x) FROM trips`,
		"time_floor":     `SELECT time_floor(__time, 'PT5M') FROM trips`,
		"millis bucket":  `SELECT millis_to_timestamp(floor(timestamp_to_millis(__time) / 60000) * 60000) FROM trips`,
		"quoted ident":   `SELECT "my col" FROM trips`,
		"case":           `SELECT CASE WHEN x > 1 THEN 'a' ELSE 'b' END FROM trips`,
	}
	for name, canonical := range renderable {
		t.Run(name, func(t *testing.T) {
			if !druidSQLRenderable(canonical) {
				t.Errorf("statement reported unrenderable: %s", canonical)
			}
		})
	}
}

// TestDruidUnrenderableCastFailsClosed verifies a cast the parser would rewrite
// into a type name Druid rejects sends the query to the object cache rather
// than to the origin as SQL it refuses.
func TestDruidUnrenderableCastFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	statement := `SELECT TIME_FLOOR(__time, 'PT5M') AS bucket, ` +
		`CAST(COUNT(*) AS BIGINT) AS trips FROM "trips" ` +
		`WHERE __time >= MILLIS_TO_TIMESTAMP(1788000000000) ` +
		`AND __time < MILLIS_TO_TIMESTAMP(1789086400000) GROUP BY 1 ORDER BY 1`
	analysis := druidSQLAnalyzer.Analyze(normalizeDruidSQL(statement), now)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("Analyze() = %s/%s, want delta before the render guard",
			analysis.Mode, analysis.Reason)
	}
	if druidSQLRenderable(analysis.Plan.CanonicalSQL) {
		t.Errorf("canonical statement passed the guard: %s", analysis.Plan.CanonicalSQL)
	}
}
