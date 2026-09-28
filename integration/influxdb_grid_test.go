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

package integration

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

const (
	gridInfluxOrigin = "127.0.0.1:8086"
	gridInfluxToken  = "Token trickster-dev-token"
	gridFluxPath     = "/api/v2/query?org=trickster-dev"
	gridInfluxQLPath = "/query?db=trickster&epoch=s&q="
	gridStatusPHit   = "phit"
)

func TestInfluxDBBucketGrids(t *testing.T) {
	// each case warms the cache with a narrow range, then requests a wider range whose
	// missing buckets are fetched on both sides of it; every bucket must match the origin
	h := configHarness(t)
	h.start(t)
	latest := waitForInfluxDBData(t, gridInfluxOrigin)
	// a fully written, minute-aligned end keeps every bucket complete and cacheable
	end := latest.Truncate(time.Minute).Add(-2 * time.Minute)

	t.Run("flux aggregateWindow stop-time labels", func(t *testing.T) {
		query := func(start, stop time.Time) string {
			return fmt.Sprintf(`{"query": %q, "type": "flux"}`, fmt.Sprintf(
				`from(bucket: "trickster") |> range(start: %d, stop: %d)`+
					` |> filter(fn: (r) => r._measurement == "cpu" and r._field == "usage_idle"`+
					` and r.cpu == "cpu-total") |> aggregateWindow(every: 1m, fn: count)`+
					` |> keep(columns: ["_time", "_value"])`, start.Unix(), stop.Unix()))
		}
		post := func(t *testing.T, addr, body string) (*http.Response, map[string]string) {
			t.Helper()
			req, err := http.NewRequest(http.MethodPost, "http://"+addr+gridFluxPath,
				strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			return gridDo(t, req, fluxBuckets)
		}
		post(t, h.BaseAddr+"/flux2", query(end.Add(-15*time.Minute), end.Add(-10*time.Minute)))
		resp, got := post(t, h.BaseAddr+"/flux2", query(end.Add(-20*time.Minute), end))
		_, want := post(t, gridInfluxOrigin, query(end.Add(-20*time.Minute), end))
		require.Equal(t, gridStatusPHit,
			parseTricksterResult(resp.Header.Get(headers.NameTricksterResult))["status"])
		require.NotEmpty(t, want)
		require.Equal(t, want, got)
	})

	t.Run("influxql offset buckets", func(t *testing.T) {
		// a 30s offset puts every bucket boundary between minute boundaries
		offsetEnd := end.Add(-30 * time.Second)
		query := func(start, stop time.Time) string {
			return url.QueryEscape(fmt.Sprintf(`SELECT count("usage_idle") FROM "cpu" WHERE `+
				`"cpu" = 'cpu-total' AND time >= '%s' AND time < '%s' GROUP BY time(1m, 30s)`,
				start.Format(time.RFC3339), stop.Format(time.RFC3339)))
		}
		get := func(t *testing.T, addr, q string) (*http.Response, map[string]string) {
			t.Helper()
			req, err := http.NewRequest(http.MethodGet, "http://"+addr+gridInfluxQLPath+q, nil)
			require.NoError(t, err)
			return gridDo(t, req, influxQLBuckets)
		}
		get(t, h.BaseAddr+"/flux2", query(offsetEnd.Add(-15*time.Minute),
			offsetEnd.Add(-10*time.Minute)))
		resp, got := get(t, h.BaseAddr+"/flux2", query(offsetEnd.Add(-20*time.Minute), offsetEnd))
		_, want := get(t, gridInfluxOrigin, query(offsetEnd.Add(-20*time.Minute), offsetEnd))
		t.Logf("result: %s", resp.Header.Get(headers.NameTricksterResult))
		require.Equal(t, gridStatusPHit,
			parseTricksterResult(resp.Header.Get(headers.NameTricksterResult))["status"])
		require.NotEmpty(t, want)
		require.Equal(t, want, got)
	})
}

func gridDo(t *testing.T, req *http.Request,
	decode func(*testing.T, []byte) map[string]string,
) (*http.Response, map[string]string) {
	t.Helper()
	req.Header.Set(headers.NameAuthorization, gridInfluxToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected response: %s", string(b))
	return resp, decode(t, b)
}

func fluxBuckets(t *testing.T, body []byte) map[string]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	require.NoError(t, err)
	out := make(map[string]string)
	timeCol, valueCol := -1, -1
	for _, rec := range records {
		if len(rec) == 0 || strings.HasPrefix(rec[0], "#") {
			continue
		}
		if i := slices.Index(rec, "_time"); i >= 0 {
			timeCol, valueCol = i, slices.Index(rec, "_value")
			continue
		}
		if timeCol >= 0 && valueCol >= 0 && len(rec) > max(timeCol, valueCol) {
			out[rec[timeCol]] = rec[valueCol]
		}
	}
	return out
}

func influxQLBuckets(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var doc struct {
		Results []struct {
			Series []struct {
				Values [][]json.Number `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	require.NoError(t, d.Decode(&doc))
	out := make(map[string]string)
	for _, result := range doc.Results {
		for _, series := range result.Series {
			for _, v := range series.Values {
				if len(v) == 2 {
					ts, err := strconv.ParseInt(v[0].String(), 10, 64)
					require.NoError(t, err)
					out[time.Unix(ts, 0).UTC().Format(time.RFC3339)] = v[1].String()
				}
			}
		}
	}
	return out
}
