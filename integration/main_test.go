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
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/daemon"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}


type expectedStartError struct {
	ErrorContains *string
	Error         *error
}

func startTrickster(t *testing.T, ctx context.Context, expected expectedStartError, args ...string) {
	err := daemon.Start(ctx, args...)
	if expected.Error != nil {
		require.ErrorIs(t, err, *expected.Error)
	} else if expected.ErrorContains != nil {
		require.ErrorContains(t, err, *expected.ErrorContains)
	} else {
		require.NoError(t, err)
	}
}

func checkTricksterMetrics(t *testing.T, address string) []string {
	url := "http://" + filepath.Join(address, "metrics")
	t.Log("Checking Trickster metrics at", url)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected 200 OK from Trickster metrics endpoint")
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	lines := strings.Split(string(b), "\n")
	return slices.DeleteFunc(lines, func(s string) bool {
		if strings.HasPrefix(s, "#") || s == "" {
			return true
		}
		return false
	})
}

func checkTrickster(t *testing.T, address string, path string, expectedStatus int) (string, http.Header) {
	resp, err := http.Get("http://" + filepath.Join(address, path))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, expectedStatus, resp.StatusCode, "Expected status code %d from Trickster at %s", expectedStatus, path)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body), resp.Header.Clone()
}

func waitForTrickster(t *testing.T, addr string, path ...string) {
	t.Helper()
	p := "/metrics"
	if len(path) > 0 {
		p = path[0]
	}
	url := "http://" + addr + p
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get(url)
		if !assert.NoError(collect, err) {
			return
		}
		resp.Body.Close()
		assert.Equal(collect, 200, resp.StatusCode)
	}, 10*time.Second, 250*time.Millisecond, "endpoint did not become ready: "+url)
}

func waitForPrometheusData(t *testing.T, prometheusAddr string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		now := time.Now()
		step := 15 * time.Second
		// Truncate end to step boundary to match DPC's NormalizeExtent.
		end := now.Truncate(step)
		start := end.Add(-5 * time.Minute)
		qp := url.Values{
			"query": {"up"},
			"start": {strconv.FormatInt(start.Unix(), 10)},
			"end":   {strconv.FormatInt(end.Unix(), 10)},
			"step":  {"15"},
		}
		resp, err := http.Get("http://" + prometheusAddr + "/api/v1/query_range?" + qp.Encode())
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		var pr promResponse
		if !assert.NoError(collect, json.Unmarshal(b, &pr)) {
			return
		}
		var qd promQueryData
		if !assert.NoError(collect, json.Unmarshal(pr.Data, &qd)) {
			return
		}
		var series []json.RawMessage
		if !assert.NoError(collect, json.Unmarshal(qd.Result, &series)) {
			return
		}
		assert.NotEmpty(collect, series, "waiting for step-aligned range data")
	}, 60*time.Second, 2*time.Second, "Prometheus range data never became available at step alignment")
}

func waitForClickHouseData(t *testing.T, clickhouseAddr string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get("http://" + clickhouseAddr + "/?query=" +
			url.QueryEscape("SELECT count() FROM trips"))
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		if !assert.Equal(collect, 200, resp.StatusCode,
			"clickhouse not ready: %s", strings.TrimSpace(string(b))) {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if !assert.NoError(collect, err) {
			return
		}
		assert.Greater(collect, n, 0, "waiting for ClickHouse seed data")
	}, 5*time.Minute, 2*time.Second, "ClickHouse trips data never became available")
}

func waitForInfluxDBData(t *testing.T, influxAddr string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := http.NewRequest("POST",
			"http://"+influxAddr+"/api/v2/query?org=trickster-dev",
			strings.NewReader(`{"query": "from(bucket: \"trickster\") |> range(start: -5m) |> limit(n: 1)", "type": "flux"}`))
		if !assert.NoError(collect, err) {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token trickster-dev-token")
		resp, err := http.DefaultClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		assert.Greater(collect, len(lines), 1, "waiting for Telegraf to write data to InfluxDB")
	}, 30*time.Second, 2*time.Second, "InfluxDB data never became available")
}

type promResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

type promQueryData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

func queryTricksterProm(t *testing.T, address, backend, apiPath string, params url.Values) (promResponse, http.Header) {
	t.Helper()
	u := "http://" + address + "/" + backend + apiPath
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	// ALB mechanisms may merge headers in ways that confuse Go's auto-decompression.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		require.NoError(t, err)
		defer gr.Close()
		reader = gr
	}
	b, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status %d: %s", resp.StatusCode, string(b))
	var pr promResponse
	require.NoError(t, json.Unmarshal(b, &pr))
	return pr, resp.Header.Clone()
}

func parseTricksterResult(header string) map[string]string {
	result := make(map[string]string)
	for part := range strings.SplitSeq(header, "; ") {
		if i := strings.Index(part, "="); i > 0 && i < len(part)-1 {
			result[part[:i]] = part[i+1:]
		}
	}
	return result
}
