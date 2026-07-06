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

package elasticsearch

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const searchBody = `{
  "size": 0,
  "query": {
    "bool": {
      "filter": [
        {
          "range": {
            "@timestamp": {
              "gte": 1704067200000,
              "lte": 1704067800000,
              "format": "epoch_millis"
            }
          }
        }
      ]
    }
  },
  "aggs": {
    "2": {
      "date_histogram": {
        "field": "@timestamp",
        "fixed_interval": "1m",
        "min_doc_count": 0,
        "extended_bounds": {
          "min": 1704067200000,
          "max": 1704067800000
        }
      },
      "aggs": {
        "1": {
          "avg": {
            "field": "value"
          }
        }
      }
    }
  }
}`

func TestParseTimeRangeQuerySearch(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/metrics/_search", strings.NewReader(searchBody))
	trq, ro, canOPC, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatalf("ParseTimeRangeQuery returned error: %v", err)
	}
	if !canOPC {
		t.Fatal("expected canOPC fallback to be true")
	}
	if ro == nil || ro.ProviderRequest == nil {
		t.Fatal("expected provider request in request options")
	}
	if trq.Step != time.Minute {
		t.Fatalf("step = %s, want 1m", trq.Step)
	}
	if got, want := trq.Extent.Start.UnixMilli(), int64(1704067200000); got != want {
		t.Fatalf("start = %d, want %d", got, want)
	}
	if got, want := trq.Extent.End.UnixMilli(), int64(1704067800000); got != want {
		t.Fatalf("end = %d, want %d", got, want)
	}
	if trq.TimestampDefinition.Name != "@timestamp" {
		t.Fatalf("timestamp field = %q, want @timestamp", trq.TimestampDefinition.Name)
	}
	if !strings.Contains(trq.Statement, rangeStartToken) || !strings.Contains(trq.Statement, rangeEndToken) {
		t.Fatalf("normalized statement does not contain range tokens: %s", trq.Statement)
	}
}

func TestParseTimeRangeQueryGetBody(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodGet, "/metrics/_search", strings.NewReader(searchBody))
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatalf("ParseTimeRangeQuery returned error: %v", err)
	}
	if got, want := trq.Step, time.Minute; got != want {
		t.Fatalf("step = %s, want %s", got, want)
	}
}

func TestParseTimeRangeQuerySourceParam(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodGet,
		"/metrics/_search?source="+url.QueryEscape(searchBody), nil)
	trq, ro, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatalf("ParseTimeRangeQuery returned error: %v", err)
	}
	plan := ro.ProviderRequest.(*RequestPlan)
	if !plan.SourceBody {
		t.Fatal("expected source query parameter to be marked as source body")
	}
	next := &timeseries.Extent{
		Start: trq.Extent.Start.Add(time.Minute),
		End:   trq.Extent.Start.Add(2 * time.Minute),
	}
	c.SetExtent(r, trq, next)
	if got := r.URL.Query().Get(queryParamSource); !strings.Contains(got, "1704067260000") {
		t.Fatalf("rewritten source query does not include new start: %s", got)
	}
}

func TestSetExtentSearchBody(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/metrics/_search", strings.NewReader(searchBody))
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	next := &timeseries.Extent{
		Start: time.UnixMilli(1704067320000),
		End:   time.UnixMilli(1704067440000),
	}
	c.SetExtent(r, trq, next)
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	rangeNode := out["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)[0].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)
	if got, want := int64(rangeNode["gte"].(float64)), int64(1704067320000); got != want {
		t.Fatalf("gte = %d, want %d", got, want)
	}
	if got, want := int64(rangeNode["lte"].(float64)), int64(1704067440000); got != want {
		t.Fatalf("lte = %d, want %d", got, want)
	}
	bounds := out["aggs"].(map[string]any)["2"].(map[string]any)["date_histogram"].(map[string]any)["extended_bounds"].(map[string]any)
	if got, want := int64(bounds["min"].(float64)), int64(1704067320000); got != want {
		t.Fatalf("extended min = %d, want %d", got, want)
	}
	if got, want := int64(bounds["max"].(float64)), int64(1704067440000); got != want {
		t.Fatalf("extended max = %d, want %d", got, want)
	}
}

func TestParseUnsupportedSearchFallsBackToObjectCache(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/metrics/_search",
		strings.NewReader(`{"query":{"match_all":{}}}`))
	trq, _, canOPC, err := c.ParseTimeRangeQuery(r)
	if err == nil {
		t.Fatal("expected unsupported query error")
	}
	if !canOPC {
		t.Fatal("expected object cache fallback")
	}
	if trq == nil || trq.CacheKeyElements["query"] == "" {
		t.Fatal("expected normalized cache key elements for object cache fallback")
	}
}

func TestParseMSearchPlan(t *testing.T) {
	searchLine := compactJSON(t, searchBody)
	body := bytes.NewBuffer(nil)
	body.WriteString(`{"index":"metrics"}` + "\n")
	body.WriteString(searchLine + "\n")
	body.WriteString(`{"index":"metrics"}` + "\n")
	body.WriteString(searchLine + "\n")

	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/_msearch", body)
	trq, ro, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatalf("ParseTimeRangeQuery returned error: %v", err)
	}
	plan := ro.ProviderRequest.(*RequestPlan)
	if plan.Kind != requestKindMSearch {
		t.Fatalf("plan kind = %d, want msearch", plan.Kind)
	}
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	next := &timeseries.Extent{
		Start: trq.Extent.Start.Add(2 * time.Minute),
		End:   trq.Extent.Start.Add(3 * time.Minute),
	}
	c.SetExtent(r, trq, next)
	lines := splitNDJSONFromString(t, r.Body)
	if len(lines) != 4 {
		t.Fatalf("rewritten msearch lines = %d, want 4", len(lines))
	}
}

func splitNDJSONFromString(t *testing.T, body any) [][]byte {
	t.Helper()
	var buf bytes.Buffer
	switch x := body.(type) {
	case interface{ Read([]byte) (int, error) }:
		_, _ = buf.ReadFrom(x)
	default:
		t.Fatalf("unsupported body type %T", body)
	}
	return splitNDJSON(buf.Bytes())
}

func compactJSON(t *testing.T, input string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(input)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
