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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
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
