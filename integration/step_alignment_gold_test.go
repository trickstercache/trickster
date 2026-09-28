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
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const (
	goldDruidNative = `{"queryType":"timeseries","dataSource":"trips","granularity":"five_minute",` +
		`"intervals":["%s/%s"],"aggregations":[{"type":"count","name":"trips"}]}`
	goldInfluxQLV3Path = "/api/v3/query_influxql"
	goldInfluxSQLPath  = "/api/v3/query_sql"
	goldValueTolerance = 1e-9
)

var goldModes = []timeseries.StepAlignment{
	timeseries.StepAlignmentTruncate, timeseries.StepAlignmentDrop, timeseries.StepAlignmentPartial,
	timeseries.StepAlignmentPartialStart, timeseries.StepAlignmentPartialEnd,
}

type goldCase struct {
	// one query shape, sent to Trickster over an unaligned range and to the origin over the range
	// each mode answers, whose values must match by label
	name, backend, path, origin string
	step                        time.Duration
	from, to                    time.Time
	opts                        func(from, to time.Time) []requestOption
	values                      func(t *testing.T, body []byte) map[int64]float64
}

func TestStepAlignmentGold(t *testing.T) {
	waitForClickHouseData(t, offClickHouseAddr)
	waitForInfluxDB3Data(t, offInfluxDB3Addr)
	latest := waitForInfluxDBData(t, offInfluxDB2Addr)
	backends := []string{offClickHouseBackend, offDruidBackend, offInfluxBackend, offFlightBackend}
	h := configHarness(t, func(c *tkconfig.Config) {
		// one copy of each backend per mode, reached over HTTP only
		for _, name := range backends {
			for _, mode := range goldModes {
				o := c.Backends[name].Clone()
				o.Name, o.StepAlignment, o.ListenerNames = goldBackend(name, mode), mode, nil
				for _, l := range c.Backends[name].ListenerNames {
					if lo := c.Listeners[l]; lo == nil || lo.Protocol == "" || lo.Protocol == listener.ProtocolHTTP {
						o.ListenerNames = append(o.ListenerNames, l)
					}
				}
				c.Backends[o.Name] = o
			}
		}
	})
	h.start(t)

	now := time.Now().UTC()
	tripsFrom, tripsTo := offRange(now.Add(-26*time.Hour), time.Hour)
	influxFrom, influxTo := offRange(latest.Add(-20*time.Minute), 10*time.Minute)
	i3From, i3To := offRange(now.Add(-20*time.Minute), 10*time.Minute)
	influxQL := func(from, to time.Time) string {
		return fmt.Sprintf(offInfluxQL, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	v3 := func(q string) []requestOption {
		return []requestOption{withParams(url.Values{"db": {offInfluxDB}, "format": {"json"}, "q": {q}})}
	}
	for _, tc := range []goldCase{
		{
			name: "clickhouse", backend: offClickHouseBackend, path: "/", origin: offClickHouseAddr,
			step: 5 * time.Minute, from: tripsFrom, to: tripsTo,
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{"query": {
					fmt.Sprintf(offClickHouseSQL, from.Unix(), to.Unix()),
				}})}
			},
			values: func(t *testing.T, body []byte) map[int64]float64 {
				var doc struct {
					Data []map[string]any `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &doc), "%.240s", body)
				return goldRows(t, doc.Data, "t", "cnt")
			},
		},
		{
			name: "druid native", backend: offDruidBackend, path: "/druid/v2", origin: offDruidAddr,
			step: 5 * time.Minute, from: tripsFrom, to: tripsTo,
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withBody(headers.ValueApplicationJSON, fmt.Sprintf(goldDruidNative,
					from.Format(time.RFC3339), to.Format(time.RFC3339)))}
			},
			values: func(t *testing.T, body []byte) map[int64]float64 {
				var rows []struct {
					Timestamp string         `json:"timestamp"`
					Result    map[string]any `json:"result"`
				}
				require.NoError(t, json.Unmarshal(body, &rows), "%.240s", body)
				flat := make([]map[string]any, len(rows))
				for i, row := range rows {
					flat[i] = map[string]any{"timestamp": row.Timestamp, "trips": row.Result["trips"]}
				}
				return goldRows(t, flat, "timestamp", "trips")
			},
		},
		{
			name: "druid sql", backend: offDruidBackend, path: "/druid/v2/sql", origin: offDruidAddr,
			step: 5 * time.Minute, from: tripsFrom, to: tripsTo,
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withBody(headers.ValueApplicationJSON,
					fmt.Sprintf(offDruidSQL, from.UnixMilli(), to.UnixMilli()))}
			},
			values: func(t *testing.T, body []byte) map[int64]float64 {
				var rows []map[string]any
				require.NoError(t, json.Unmarshal(body, &rows), "%.240s", body)
				return goldRows(t, rows, "bucket", "trips")
			},
		},
		{
			name: "influxql", backend: offInfluxBackend, path: "/query", origin: offInfluxDB2Addr,
			step: 10 * time.Second, from: influxFrom, to: influxTo,
			opts: func(from, to time.Time) []requestOption {
				return []requestOption{withParams(url.Values{
					"db": {offInfluxDB}, "u": {offInfluxUser}, "p": {offInfluxTokenVal}, "q": {influxQL(from, to)},
				})}
			},
			values: influxQLValues,
		},
		{
			name: "influxql over v3", backend: offFlightBackend, path: goldInfluxQLV3Path, origin: offInfluxDB3Addr,
			step: 10 * time.Second, from: i3From, to: i3To,
			opts: func(from, to time.Time) []requestOption { return v3(influxQL(from, to)) },
			values: func(t *testing.T, body []byte) map[int64]float64 {
				var rows []map[string]any
				require.NoError(t, json.Unmarshal(body, &rows), "%.240s", body)
				return goldRows(t, rows, "time", "mean")
			},
		},
		{
			name: "influxdb3 sql", backend: offFlightBackend, path: goldInfluxSQLPath, origin: offInfluxDB3Addr,
			step: 10 * time.Second, from: i3From, to: i3To,
			opts: func(from, to time.Time) []requestOption {
				return v3(fmt.Sprintf(offInflux3SQL, from.Format(time.RFC3339), to.Format(time.RFC3339)))
			},
			values: func(t *testing.T, body []byte) map[int64]float64 {
				var rows []map[string]any
				require.NoError(t, json.Unmarshal(body, &rows), "%.240s", body)
				return goldRows(t, rows, "time", "usage_idle")
			},
		},
	} {
		for _, mode := range goldModes {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				// the origin's own answer over the range the mode answers, whose partial edge buckets
				// hold only the rows inside the client's range
				from, to := goldRange(mode, tc.from, tc.to, tc.step)
				resp, body := tricksterHarness{BaseAddr: tc.origin}.do(t, tc.path, tc.opts(from, to)...)
				require.Equal(t, http.StatusOK, resp.StatusCode, "%.240s", body)
				want := tc.values(t, body)
				require.NotEmpty(t, want, "the origin has no data for %s", tc.name)
				start, end := tc.from.Truncate(tc.step), tc.to.Truncate(tc.step)
				_, partialEnd := mode.Edges()
				for _, attempt := range []string{"first", "repeat"} {
					resp, body := h.do(t, "/"+goldBackend(tc.backend, mode)+tc.path, tc.opts(tc.from, tc.to)...)
					require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %.240s", attempt, body)
					got := tc.values(t, body)
					requireSameBuckets(t, want, got, attempt)
					// the edges come from partial buckets only under the partial modes
					result := resp.Header.Get(headers.NameTricksterResult)
					if mode == timeseries.StepAlignmentTruncate || mode == timeseries.StepAlignmentDrop {
						require.NotContains(t, result, keys.PartialBuckets, attempt)
					} else {
						require.Contains(t, result, keys.PartialBuckets, attempt)
					}
					// no mode shows a bucket past the client's range, and only a partial end shows the last
					_, sawEnd := got[end.Unix()]
					require.Equal(t, partialEnd == timeseries.EdgePartial && sawEnd, sawEnd, attempt)
					for label := range got {
						require.False(t, label < start.Unix() || label > end.Unix(), "%s: label %d", attempt, label)
					}
				}
			})
		}
	}
}

func goldBackend(name string, mode timeseries.StepAlignment) string {
	return name + "-" + mode.String()
}

func goldRange(mode timeseries.StepAlignment, from, to time.Time, step time.Duration) (time.Time, time.Time) {
	// the raw range whose answer the mode serves for [from, to): truncate reads the whole first
	// bucket, drop none of it, and only the partial ends read into the last bucket
	floorFrom, ceilFrom, floorTo := from.Truncate(step), from.Truncate(step).Add(step), to.Truncate(step)
	switch mode {
	case timeseries.StepAlignmentTruncate:
		return floorFrom, floorTo
	case timeseries.StepAlignmentDrop:
		return ceilFrom, floorTo
	case timeseries.StepAlignmentPartialStart:
		return from, floorTo
	case timeseries.StepAlignmentPartialEnd:
		return floorFrom, to
	}
	return from, to
}

func requireSameBuckets(t *testing.T, want, got map[int64]float64, attempt string) {
	t.Helper()
	// values match by label; row order and number formatting may differ
	require.Len(t, got, len(want), "%s: got %v want %v", attempt, got, want)
	for label, value := range want {
		g, ok := got[label]
		require.True(t, ok, "%s: missing bucket %d; got %v want %v", attempt, label, got, want)
		require.LessOrEqual(t, math.Abs(g-value), goldValueTolerance*max(1, math.Abs(value)),
			"%s: bucket %d is %v, want %v", attempt, label, g, value)
	}
}

func goldRows(t *testing.T, rows []map[string]any, timeKey, valueKey string) map[int64]float64 {
	t.Helper()
	out := make(map[int64]float64, len(rows))
	for _, row := range rows {
		value, ok := goldNumber(row[valueKey])
		if !ok {
			// a bucket with no rows answers null, which isn't a value to compare
			continue
		}
		out[goldTime(t, row[timeKey])] = value
	}
	return out
}

func influxQLValues(t *testing.T, body []byte) map[int64]float64 {
	t.Helper()
	var doc struct {
		Results []struct {
			Series []struct {
				Values [][]any `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &doc), "%.240s", body)
	var rows []map[string]any
	for _, result := range doc.Results {
		for _, series := range result.Series {
			for _, v := range series.Values {
				rows = append(rows, map[string]any{"time": v[0], "value": v[1]})
			}
		}
	}
	return goldRows(t, rows, "time", "value")
}

func goldNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

func goldTime(t *testing.T, v any) int64 {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "timestamp %v", v)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if ts, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return ts.Unix()
		}
	}
	require.Failf(t, "unparsable timestamp", "%q", s)
	return 0
}
