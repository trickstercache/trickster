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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const searchResponse = `{
  "took": 1,
  "timed_out": false,
  "hits": {
    "total": {
      "value": 0,
      "relation": "eq"
    },
    "hits": []
  },
  "aggregations": {
    "2": {
      "buckets": [
        {
          "key": 1704067200000,
          "doc_count": 1,
          "1": {
            "value": 10
          }
        },
        {
          "key": 1704067260000,
          "doc_count": 2,
          "1": {
            "value": 20
          }
        }
      ]
    }
  }
}`

func TestUnmarshalMarshalSearchResponse(t *testing.T) {
	trq, ro := parseSearchForModelTest(t)
	ts, err := UnmarshalTimeseries([]byte(searchResponse), trq)
	if err != nil {
		t.Fatalf("UnmarshalTimeseries returned error: %v", err)
	}
	ds := ts.(*dataset.DataSet)
	if got := ds.ValueCount(); got != 2 {
		t.Fatalf("value count = %d, want 2", got)
	}
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(ts, ro, http.StatusOK, &buf); err != nil {
		t.Fatalf("MarshalTimeseriesWriter returned error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	buckets := out["aggregations"].(map[string]any)["2"].(map[string]any)["buckets"].([]any)
	if len(buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(buckets))
	}
	if got := int64(buckets[0].(map[string]any)["key"].(float64)); got != 1704067200000 {
		t.Fatalf("first key = %d, want 1704067200000", got)
	}
}

func TestUnmarshalMarshalMSearchResponse(t *testing.T) {
	trq, ro := parseMSearchForModelTest(t)
	resp := `{"responses":[` + searchResponse + `,` + searchResponse + `]}`
	ts, err := UnmarshalTimeseries([]byte(resp), trq)
	if err != nil {
		t.Fatalf("UnmarshalTimeseries returned error: %v", err)
	}
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(ts, ro, http.StatusOK, &buf); err != nil {
		t.Fatalf("MarshalTimeseriesWriter returned error: %v", err)
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if got := len(out["responses"]); got != 2 {
		t.Fatalf("responses = %d, want 2", got)
	}
	if got := int(out["responses"][0]["status"].(float64)); got != http.StatusOK {
		t.Fatalf("response status = %d, want 200", got)
	}
}

func TestUnmarshalMSearchResponseRequiresOneResponsePerSearch(t *testing.T) {
	trq, _ := parseMSearchForModelTest(t)
	resp := `{"responses":[` + searchResponse + `]}`
	if _, err := UnmarshalTimeseries([]byte(resp), trq); err == nil {
		t.Fatal("expected response-count mismatch to fail instead of fabricating an empty search result")
	}
}

func parseSearchForModelTest(t *testing.T) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions) {
	t.Helper()
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/metrics/_search", strings.NewReader(searchBody))
	trq, ro, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	return trq, ro
}

func parseMSearchForModelTest(t *testing.T) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions) {
	t.Helper()
	searchLine := compactJSON(t, searchBody)
	var body bytes.Buffer
	body.WriteString(`{"index":"metrics"}` + "\n")
	body.WriteString(searchLine + "\n")
	body.WriteString(`{"index":"metrics"}` + "\n")
	body.WriteString(searchLine + "\n")
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "/_msearch", &body)
	trq, ro, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	return trq, ro
}
