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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func parsedRewriteRequest(t *testing.T) (*Client, *http.Request, *timeseries.TimeRangeQuery) {
	t.Helper()
	body := `{"queryType":"timeseries","dataSource":"wiki","intervals":["2024-01-01T00:00:00Z/2024-01-01T00:03:00Z"],"granularity":"minute","context":{"queryId":"keep-upstream","priority":7,"skipEmptyBuckets":true}}`
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", strings.NewReader(body))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	c := &Client{}
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	return c, r, trq
}

func TestSetExtentRendersHalfOpenIntervalAndPreservesContext(t *testing.T) {
	c, r, trq := parsedRewriteRequest(t)
	extent := &timeseries.Extent{
		Start: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
		End:   time.Date(2024, 1, 1, 1, 2, 0, 0, time.UTC),
	}
	statement := trq.Statement
	if err := c.SetExtent(r, trq, extent); err != nil {
		t.Fatal(err)
	}
	body, err := request.GetBody(r)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	intervals := document["intervals"].([]any)
	if intervals[0] != "2024-01-01T01:00:00Z/2024-01-01T01:03:00Z" {
		t.Fatalf("interval = %v", intervals[0])
	}
	context := document["context"].(map[string]any)
	if context["queryId"] != "keep-upstream" || context["priority"] != float64(7) ||
		context["skipEmptyBuckets"] != true {
		t.Fatalf("upstream context changed: %v", context)
	}
	if trq.Statement != statement {
		t.Fatal("SetExtent mutated the canonical statement")
	}

	second := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", nil)
	second.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	secondExtent := &timeseries.Extent{Start: extent.Start.Add(time.Hour), End: extent.End.Add(time.Hour)}
	if err := c.SetExtent(second, trq, secondExtent); err != nil {
		t.Fatal(err)
	}
	secondBody, _ := request.GetBody(second)
	if string(body) == string(secondBody) {
		t.Fatal("independent extent renders produced the same body")
	}
}

func TestSetExtentFailsClosed(t *testing.T) {
	c := &Client{}
	extent := &timeseries.Extent{Start: time.Now(), End: time.Now()}
	tests := []struct {
		name string
		r    *http.Request
		trq  *timeseries.TimeRangeQuery
		ext  *timeseries.Extent
		want error
	}{
		{"nil request", nil, &timeseries.TimeRangeQuery{}, extent, errInvalidRewrite},
		{"nil query", httptest.NewRequest(http.MethodPost, "/", nil), nil, extent, errInvalidRewrite},
		{"nil extent", httptest.NewRequest(http.MethodPost, "/", nil), &timeseries.TimeRangeQuery{}, nil, errInvalidRewrite},
		{"missing plan", httptest.NewRequest(http.MethodPost, "/", nil), &timeseries.TimeRangeQuery{Step: time.Minute}, extent, errMissingQueryPlan},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := c.SetExtent(test.r, test.trq, test.ext); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}

	c, r, trq := parsedRewriteRequest(t)
	trq.Step = 0
	if err := c.SetExtent(r, trq, extent); !errors.Is(err, errInvalidQueryStep) {
		t.Fatalf("got %v, want invalid step", err)
	}

	_, r, trq = parsedRewriteRequest(t)
	invalidOrder := &timeseries.Extent{Start: extent.End.Add(time.Minute), End: extent.End}
	if err := c.SetExtent(r, trq, invalidOrder); !errors.Is(err, errInvalidRewrite) {
		t.Fatalf("got %v, want invalid extent order", err)
	}
	tooLate := &timeseries.Extent{
		Start: time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC),
		End:   time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC),
	}
	if err := c.SetExtent(r, trq, tooLate); !errors.Is(err, errInvalidRewrite) {
		t.Fatalf("got %v, want out-of-range extent error", err)
	}
}
