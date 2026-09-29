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
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const promDropStep = 15 * time.Second

func TestPrometheusDrop(t *testing.T) {
	// drop evaluates only at grid instants inside the range: the origin's own points from ceil(start),
	// never one before the start, and no fast forward point
	waitForPrometheusData(t, offPromAddr)
	backend := goldBackend(offPromBackend, timeseries.StepAlignmentDrop)
	h := configHarness(t, func(c *tkconfig.Config) {
		o := c.Backends[offPromBackend].Clone()
		o.Name, o.StepAlignment, o.IsDefault = backend, timeseries.StepAlignmentDrop, false
		c.Backends[backend] = o
	})
	h.start(t)
	start, end := offRange(time.Now().Add(-2*time.Hour), 30*time.Minute)
	first := start.Truncate(promDropStep).Add(promDropStep)
	params := func(from time.Time) requestOption {
		return withParams(url.Values{
			"query": {"up"}, "start": {strconv.FormatInt(from.Unix(), 10)},
			"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.Itoa(int(promDropStep.Seconds()))},
		})
	}
	// the origin, asked from the first instant, answers the points drop serves
	resp, body := tricksterHarness{BaseAddr: offPromAddr}.do(t, "/api/v1/query_range", params(first))
	require.Equal(t, http.StatusOK, resp.StatusCode, "%.240s", body)
	want := promInstants(t, body)
	require.NotEmpty(t, want)
	for _, attempt := range []string{"first", "repeat"} {
		resp, body := h.do(t, "/"+backend+"/api/v1/query_range", params(start))
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %.240s", attempt, body)
		got := promInstants(t, body)
		require.Equal(t, want, got, attempt)
		for instant := range got {
			require.GreaterOrEqual(t, instant, start.Unix(), "%s: a point before the range's start", attempt)
		}
		require.Equal(t, "off", parseTricksterResult(resp.Header.Get(headers.NameTricksterResult))[keys.FFStatus],
			attempt)
	}
	// a query's directive chooses drop on a backend left at its default, partial_end
	for _, attempt := range []string{"first", "repeat"} {
		resp, body := h.do(t, "/"+offPromBackend+"/api/v1/query_range", withParams(url.Values{
			"query": {"up # trickster-step-align:drop"}, "start": {strconv.FormatInt(start.Unix(), 10)},
			"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.Itoa(int(promDropStep.Seconds()))},
		}))
		require.Equal(t, http.StatusOK, resp.StatusCode, "directive %s: %.240s", attempt, body)
		require.Equal(t, want, promInstants(t, body), "directive %s", attempt)
		require.Equal(t, "off", parseTricksterResult(resp.Header.Get(headers.NameTricksterResult))[keys.FFStatus],
			"directive %s", attempt)
	}
	// ranges shorter than a step: none holding no grid instant, and the one it holds, anchored on the
	// origin's earliest point so a gap in its data can't empty the expected instant
	grid := time.Unix(slices.Min(slices.Collect(maps.Keys(want))), 0).Add(-promDropStep)
	for _, test := range []struct {
		name     string
		from, to time.Duration
		want     []int64
	}{
		{"no grid instant", 7 * time.Second, 12 * time.Second, nil},
		{"one grid instant", 7 * time.Second, 20 * time.Second, []int64{grid.Add(promDropStep).Unix()}},
	} {
		for _, attempt := range []string{"first", "repeat"} {
			resp, body := h.do(t, "/"+backend+"/api/v1/query_range", withParams(url.Values{
				"query": {"up"}, "start": {strconv.FormatInt(grid.Add(test.from).Unix(), 10)},
				"end": {strconv.FormatInt(grid.Add(test.to).Unix(), 10)}, "step": {strconv.Itoa(int(promDropStep.Seconds()))},
			}))
			require.Equal(t, http.StatusOK, resp.StatusCode, "%s %s: %.240s", test.name, attempt, body)
			var instants []int64
			for instant := range promInstants(t, body) {
				instants = append(instants, instant)
			}
			require.ElementsMatch(t, test.want, instants, "%s %s", test.name, attempt)
		}
	}
}

func promInstants(t *testing.T, body []byte) map[int64]int {
	t.Helper()
	// how many series have a point at each instant
	var doc struct {
		Data struct {
			Result []struct {
				Values [][]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &doc), "%.240s", body)
	out := make(map[int64]int)
	for _, series := range doc.Data.Result {
		for _, v := range series.Values {
			instant, ok := v[0].(float64)
			require.True(t, ok, "timestamp %v", v[0])
			out[int64(instant)]++
		}
	}
	return out
}
