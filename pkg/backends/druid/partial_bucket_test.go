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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const (
	bucketTimestamp = "2006-01-02T15:04:05.000Z"
	sqlTimestamp    = "2006-01-02 15:04:05"
	sqlLower        = "__time >= TIMESTAMP '"
	sqlUpper        = "__time < TIMESTAMP '"
	bucketSQL       = "SELECT TIME_FLOOR(__time, 'PT1M') AS bucket, COUNT(*) AS rows FROM trips " +
		"WHERE __time >= TIMESTAMP '%s' AND __time < TIMESTAMP '%s' GROUP BY 1"
)

type bucketOrigin struct {
	// answers native and SQL queries with minute buckets labeled at their start, each counting the
	// seconds of it the request covered, as an origin with a row per second would
	mu     sync.Mutex
	ranges []string
}

func (o *bucketOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var start, end time.Time
	var err error
	if query, ok := document["query"].(string); ok {
		start, end, err = sqlRange(query)
	} else {
		intervals, _ := document["intervals"].([]any)
		interval, _ := intervals[0].(string)
		start, end, err = parseInterval(interval)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	o.ranges = append(o.ranges, start.UTC().Format(time.RFC3339)+"/"+end.UTC().Format(time.RFC3339))
	o.mu.Unlock()
	rows := []map[string]any{}
	for label := start.Truncate(time.Minute); label.Before(end); label = label.Add(time.Minute) {
		from, to := max(start.Unix(), label.Unix()), min(end.Unix(), label.Add(time.Minute).Unix())
		covered := to - from
		if _, ok := document["query"]; ok {
			rows = append(rows, map[string]any{"bucket": label.UTC().Format(bucketTimestamp), "rows": covered})
			continue
		}
		rows = append(rows, map[string]any{
			"timestamp": label.UTC().Format(bucketTimestamp), "result": map[string]any{"rows": covered},
		})
	}
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	json.NewEncoder(w).Encode(rows)
}

func (o *bucketOrigin) take() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.ranges
	o.ranges = nil
	return out
}

func sqlRange(query string) (time.Time, time.Time, error) {
	bound := func(prefix string) (time.Time, error) {
		i := strings.Index(query, prefix)
		if i < 0 || len(query) < i+len(prefix)+len(sqlTimestamp) {
			return time.Time{}, fmt.Errorf("missing %s", prefix)
		}
		return time.ParseInLocation(sqlTimestamp, query[i+len(prefix):i+len(prefix)+len(sqlTimestamp)], time.UTC)
	}
	start, err := bound(sqlLower)
	if err != nil {
		return start, start, err
	}
	end, err := bound(sqlUpper)
	return start, end, err
}

func TestPartialBuckets(t *testing.T) {
	// the interval sits 7s into its first minute and 13s into its last
	base := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	start, end := base.Add(7*time.Second), base.Add(3*time.Minute+13*time.Second)
	span := func(from, to time.Time) string { return from.Format(time.RFC3339) + "/" + to.Format(time.RFC3339) }
	want := map[string]float64{
		base.Format(bucketTimestamp): 53, base.Add(time.Minute).Format(bucketTimestamp): 60,
		base.Add(2 * time.Minute).Format(bucketTimestamp): 60, base.Add(3 * time.Minute).Format(bucketTimestamp): 13,
	}
	for _, test := range []struct {
		name, path, body string
		// native queries default to partial, and SQL to drop
		mode   timeseries.StepAlignment
		serve  func(*druidHandlerHarness, *testing.T, string, string) (*http.Response, []byte)
		values func(*testing.T, []byte) map[string]float64
	}{
		{
			"native", "/druid/v2", fmt.Sprintf(`{"queryType":"timeseries","dataSource":"trips","granularity":`+
				`"minute","intervals":[%q],"aggregations":[{"type":"count","name":"rows"}]}`, span(start, end)),
			0, (*druidHandlerHarness).query, nativeValues,
		},
		{
			"sql", "/druid/v2/sql", fmt.Sprintf(`{"query":%q,"resultFormat":"object"}`,
				fmt.Sprintf(bucketSQL, start.Format(sqlTimestamp), end.Format(sqlTimestamp))),
			timeseries.StepAlignmentPartial, (*druidHandlerHarness).sqlQuery, sqlValues,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := &bucketOrigin{}
			server := httptest.NewServer(origin)
			defer server.Close()
			harness := newDruidHandlerHarness(t, server, http.MethodPost, test.path)
			defer harness.cleanup()
			harness.resources.BackendOptions.StepAlignment = test.mode
			// under partial, the interior comes from the delta cache and each edge bucket is the
			// origin's answer over the part of it the client asked for
			for i, wantStatus := range []string{status.StatusKeyMiss, status.StatusHit} {
				response, body := test.serve(harness, t, test.path, test.body)
				require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
				require.Equal(t, wantStatus, resultStatus(t, response))
				require.Equal(t, want, test.values(t, body))
				require.Contains(t, response.Header.Get(headers.NameTricksterResult), keys.PartialBuckets)
				if i == 0 {
					require.ElementsMatch(t, []string{
						span(base.Add(time.Minute), base.Add(3*time.Minute)), span(start, base.Add(time.Minute)),
						span(base.Add(3*time.Minute), end),
					}, origin.take())
				}
			}
			require.Empty(t, origin.take(), "a repeat is answered by both caches")
		})
	}
}

func nativeValues(t *testing.T, body []byte) map[string]float64 {
	t.Helper()
	var rows []struct {
		Timestamp string             `json:"timestamp"`
		Result    map[string]float64 `json:"result"`
	}
	require.NoError(t, json.Unmarshal(body, &rows), "%s", body)
	out := map[string]float64{}
	for _, row := range rows {
		out[row.Timestamp] = row.Result["rows"]
	}
	return out
}

func sqlValues(t *testing.T, body []byte) map[string]float64 {
	t.Helper()
	var rows []struct {
		Bucket string  `json:"bucket"`
		Rows   float64 `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(body, &rows), "%s", body)
	out := map[string]float64{}
	for _, row := range rows {
		out[row.Bucket] = row.Rows
	}
	return out
}

func TestFetchPartialBucketFailures(t *testing.T) {
	c := &Client{}
	r := httptest.NewRequest(http.MethodPost, "http://trickster/druid/v2", nil)
	label := time.Unix(1704067200, 0)
	native := &timeseries.TimeRangeQuery{ParsedQuery: &model.QueryPlan{}}
	for name, test := range map[string]struct {
		r   *http.Request
		trq *timeseries.TimeRangeQuery
		pb  timeseries.PartialBucket
		err error
	}{
		"no request":   {nil, native, timeseries.PartialBucket{}, errInvalidRewrite},
		"no plan":      {r, &timeseries.TimeRangeQuery{}, timeseries.PartialBucket{}, errMissingQueryPlan},
		"nil sql plan": {r, &timeseries.TimeRangeQuery{ParsedQuery: (*model.SQLQueryPlan)(nil)}, timeseries.PartialBucket{}, errMissingQueryPlan},
		// native intervals are half-open and closed
		"open interval": {r, native, timeseries.PartialBucket{Lower: label}, backends.ErrPartialBucketsUnsupported},
		"unrenderable":  {r, native, timeseries.PartialBucket{Lower: label, Upper: label.Add(time.Minute)}, errRenderQuery},
		"sql":           {r, &timeseries.TimeRangeQuery{ParsedQuery: model.NewSQLQueryPlan(nil, nil)}, timeseries.PartialBucket{}, errRenderQuery},
	} {
		t.Run(name, func(t *testing.T) {
			_, st, err := c.FetchPartialBucket(test.r, test.trq, test.pb, false)
			require.ErrorIs(t, err, test.err)
			require.Equal(t, status.LookupStatusError, st)
		})
	}
}
