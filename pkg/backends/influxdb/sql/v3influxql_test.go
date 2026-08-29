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

package sql

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

const v3InfluxQLQuery = `SELECT mean(usage) FROM cpu WHERE time >= now() - 1h GROUP BY time(1m), "host"`

func influxqlJSONPost(t *testing.T, body string) *http.Request {
	t.Helper()
	b := []byte(body)
	return &http.Request{
		Method:        http.MethodPost,
		URL:           &url.URL{Path: "/api/v3/query_influxql"},
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(b)),
		ContentLength: int64(len(b)),
	}
}

// TestParseV3InfluxQLJSONBody verifies a JSON-bodied v3 InfluxQL POST is
// parsed into a cacheable time-range query (previously it fell through
// uncached because the v1 parser reads only form/URL params).
func TestParseV3InfluxQLJSONBody(t *testing.T) {
	body, _ := json.Marshal(map[string]string{
		"db": "prod", "q": v3InfluxQLQuery, "format": "csv",
	})
	r := influxqlJSONPost(t, string(body))
	trq, rlo, canOPC, err := ParseV3InfluxQL(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if !canOPC {
		t.Error("expected object-cache eligibility")
	}
	if trq.Step != time.Minute {
		t.Errorf("step = %s, want 1m", trq.Step)
	}
	if rlo.OutputFormat != iofmt.V3OutputCSV {
		t.Errorf("output format = %d, want csv", rlo.OutputFormat)
	}
	if trq.CacheKeyElements[ParamDB] != "prod" {
		t.Errorf("db missing from cache identity: %v", trq.CacheKeyElements)
	}
	if trq.TimestampDefinition.Name != DefaultTimestampField {
		t.Errorf("timestamp definition = %+v", trq.TimestampDefinition)
	}
	if len(trq.TagFieldDefintions) != 2 ||
		trq.TagFieldDefintions[0].Name != measurementField ||
		trq.TagFieldDefintions[1].Name != "host" {
		t.Errorf("tag fields = %+v", trq.TagFieldDefintions)
	}
	if trq.BackfillTolerance < trq.Step {
		t.Errorf("backfill tolerance %s below one bucket %s", trq.BackfillTolerance, trq.Step)
	}
	if _, ok := trq.ParsedQuery.(*V3InfluxQLQuery); !ok {
		t.Errorf("parsed query type = %T", trq.ParsedQuery)
	}
	if string(trq.OriginalBody) != string(body) {
		t.Errorf("original body not preserved: %s", trq.OriginalBody)
	}
}

// TestParseV3InfluxQLObjectCacheKeyUsesOriginalStatement ensures the
// object-cache fallback (e.g. mixed statements) is keyed on the original
// statement, not the time-range-zeroed tokenized statement, so different time
// windows never alias one cache entry.
func TestParseV3InfluxQLObjectCacheKeyUsesOriginalStatement(t *testing.T) {
	statement := v3InfluxQLQuery + " ; SHOW DATABASES"
	body, _ := json.Marshal(map[string]string{"q": statement})
	r := influxqlJSONPost(t, string(body))
	trq, _, canOPC, err := ParseV3InfluxQL(r, iofmt.Detect(r))
	if err == nil || !canOPC || trq == nil {
		t.Fatalf("ParseV3InfluxQL = %v/%t/%v", trq, canOPC, err)
	}
	if trq.CacheKeyElements[ParamQuery] != statement {
		t.Fatalf("object-cache key not the original statement: %v", trq.CacheKeyElements)
	}
}

// TestParseV3InfluxQLParameterized verifies params-bearing requests bypass
// delta caching with their identity and body intact, like the SQL path.
func TestParseV3InfluxQLParameterized(t *testing.T) {
	body := `{"db":"prod","q":"SELECT * FROM cpu WHERE host = $host","params":{"host":"a"}}`
	r := influxqlJSONPost(t, body)
	trq, _, canOPC, err := ParseV3InfluxQL(r, iofmt.Detect(r))
	if !errors.Is(err, ErrParameterizedQuery) || !canOPC || trq == nil {
		t.Fatalf("ParseV3InfluxQL = %v/%t/%v", trq, canOPC, err)
	}
	if trq.CacheKeyElements[ParamParams] != `host="a";` ||
		trq.CacheKeyElements[ParamDB] != "prod" {
		t.Fatalf("cache identity incomplete: %v", trq.CacheKeyElements)
	}
	if string(trq.OriginalBody) != body {
		t.Fatalf("original body not preserved: %s", trq.OriginalBody)
	}
}

// TestSetExtentV3InfluxQL verifies the upstream rewrite re-encodes the
// statement into the v3 JSON document with other fields preserved and no
// v1-only parameters injected.
func TestSetExtentV3InfluxQL(t *testing.T) {
	body, _ := json.Marshal(map[string]string{
		"db": "prod", "q": v3InfluxQLQuery, "format": "csv",
	})
	r := influxqlJSONPost(t, string(body))
	trq, _, _, err := ParseV3InfluxQL(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	inner := trq.ParsedQuery.(*V3InfluxQLQuery).Inner.(*influxql.Query)
	extent := timeseries.Extent{
		Start: time.Unix(1704067200, 0), End: time.Unix(1704070800, 0),
	}
	SetExtentV3InfluxQL(r, trq, &extent, inner)

	b, err := request.GetBody(r)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]string
	if err := json.Unmarshal(b, &document); err != nil {
		t.Fatalf("rewritten body not JSON: %v\n%s", err, b)
	}
	if document["db"] != "prod" || document["format"] != "csv" {
		t.Fatalf("body fields dropped on rewrite: %v", document)
	}
	if !strings.Contains(document["q"], "2024-01-01") {
		t.Fatalf("time range not stamped into statement: %s", document["q"])
	}
	if v := r.URL.Query(); v.Get("epoch") != "" || v.Get("chunked") != "" {
		t.Fatalf("v1 parameters injected into v3 request: %s", r.URL.RawQuery)
	}
}

// TestFormatFromAccept verifies Accept-header content negotiation maps to v3
// format names, including unsupported formats that must proxy through.
func TestFormatFromAccept(t *testing.T) {
	for _, tc := range []struct {
		accept, want string
	}{
		{"application/json", "json"},
		{"application/jsonl", "jsonl"},
		{"application/x-ndjson", "jsonl"},
		{"text/csv; q=0.9", "csv"},
		{"application/vnd.apache.parquet", "parquet"},
		{"text/plain", "pretty"},
		{"*/*", ""},
		{"", ""},
		{"text/html, text/csv", "csv"},
	} {
		if got := formatFromAccept(tc.accept); got != tc.want {
			t.Errorf("formatFromAccept(%q) = %q, want %q", tc.accept, got, tc.want)
		}
	}
}

// TestAcceptHeaderDrivesFormat verifies the Accept header selects the output
// format when no format parameter is present, and that unsupported Accept
// types mark the request for proxy-through.
func TestAcceptHeaderDrivesFormat(t *testing.T) {
	r := jsonPost(t, `{"q":"`+identityQuery+`"}`)
	r.Header.Set("Accept", "text/csv")
	_, rlo, _, err := ParseTimeRangeQuery(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if rlo.OutputFormat != iofmt.V3OutputCSV {
		t.Errorf("output format = %d, want csv", rlo.OutputFormat)
	}

	r = jsonPost(t, `{"q":"`+identityQuery+`"}`)
	r.Header.Set("Accept", "application/vnd.apache.parquet")
	if SupportedV3Format(r) {
		t.Error("parquet Accept header not marked for proxy-through")
	}

	// an explicit format parameter wins over the Accept header
	r = jsonPost(t, `{"q":"`+identityQuery+`","format":"jsonl"}`)
	r.Header.Set("Accept", "text/csv")
	_, rlo, _, err = ParseTimeRangeQuery(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if rlo.OutputFormat != iofmt.V3OutputJSONL {
		t.Errorf("output format = %d, want jsonl", rlo.OutputFormat)
	}
}

// TestOpenEndedQueryBackfillFloor verifies queries without an upper time bound
// get a backfill tolerance of at least one bucket so the still-filling final
// bucket is never cached as complete.
func TestOpenEndedQueryBackfillFloor(t *testing.T) {
	statement := "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) " +
		"FROM m WHERE time >= 1704067200 GROUP BY 1"
	r := jsonPost(t, `{"q":"`+statement+`"}`)
	trq, _, _, err := ParseTimeRangeQuery(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if trq.BackfillTolerance < time.Hour {
		t.Fatalf("backfill tolerance = %s, want >= 1h", trq.BackfillTolerance)
	}

	// a bounded query keeps the smaller default
	trq, _, _, err = ParseTimeRangeQuery(
		jsonPost(t, `{"q":"`+identityQuery+`"}`), iofmt.V3SQL)
	if err != nil {
		t.Fatal(err)
	}
	if trq.BackfillTolerance >= time.Hour {
		t.Fatalf("bounded query backfill tolerance = %s, want < 1h", trq.BackfillTolerance)
	}
}
