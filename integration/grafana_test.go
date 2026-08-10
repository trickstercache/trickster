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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const grafanaAPIBaseURL = "http://127.0.0.1:3000"

func TestGrafanaOrigin(t *testing.T) {
	waitForGrafana(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	h := grafanaIntegrationHarness(t)
	h.start(t)

	uid := "trickster-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	id := createGrafanaPrometheusDataSource(t, uid)
	t.Cleanup(func() { deleteGrafanaDataSource(t, uid) })

	end := time.Now().Add(-30 * time.Second).Truncate(15 * time.Second)
	params := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", time.Now().UnixNano())},
		"start": {strconv.FormatInt(end.Add(-time.Minute).Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {"15"},
	}
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:admin"))
	path := fmt.Sprintf("/grafana/api/datasources/proxy/%d/api/v1/query_range", id)

	resp, body := h.do(t, path, withParams(params), withHeader("Authorization", authorization))
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected Grafana proxy response: %s", body)
	requirePrometheusMatrix(t, body)
	requireTricksterResult(t, resp.Header, map[string]string{
		"engine": "DeltaProxyCache",
		"status": "kmiss",
	})

	resp, body = h.do(t, path, withParams(params), withHeader("Authorization", authorization))
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected Grafana proxy response: %s", body)
	requirePrometheusMatrix(t, body)
	requireTricksterResult(t, resp.Header, map[string]string{"status": "hit"})
}

func grafanaIntegrationHarness(t *testing.T) tricksterHarness {
	t.Helper()
	const config = `listeners:
  grafana_test:
    address: 127.0.0.1
    port: 8560
  metrics:
    address: 127.0.0.1
    port: 8561
  mgmt:
    address: 127.0.0.1
    port: 8564
backends:
  grafana:
    provider: grafana
    origin_url: http://127.0.0.1:3000
    listener_name: grafana_test
caches:
  default:
    provider: memory
`
	configPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
	return tricksterHarness{
		ConfigPath:  configPath,
		BaseAddr:    "127.0.0.1:8560",
		MetricsAddr: "127.0.0.1:8561",
	}
}

func waitForGrafana(t *testing.T) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		status, _, err := doGrafanaAPIRequest(http.MethodGet, "/api/health", nil)
		if !assert.NoError(collect, err) {
			return
		}
		assert.Equal(collect, http.StatusOK, status)
	}, time.Minute, time.Second, "Grafana did not become ready")
}

func createGrafanaPrometheusDataSource(t *testing.T, uid string) int64 {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"name": uid, "uid": uid, "type": "prometheus", "access": "proxy",
		"url":      "http://prometheus:9090",
		"jsonData": map[string]any{"httpMethod": http.MethodGet},
	})
	require.NoError(t, err)
	status, body, err := doGrafanaAPIRequest(http.MethodPost, "/api/datasources", payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "create Grafana data source: %s", body)
	var created struct {
		ID         int64 `json:"id"`
		DataSource struct {
			ID int64 `json:"id"`
		} `json:"datasource"`
	}
	require.NoError(t, json.Unmarshal(body, &created))
	if created.ID == 0 {
		created.ID = created.DataSource.ID
	}
	require.Positive(t, created.ID, "Grafana data source response: %s", body)
	return created.ID
}

func deleteGrafanaDataSource(t *testing.T, uid string) {
	t.Helper()
	status, body, err := doGrafanaAPIRequest(http.MethodDelete, "/api/datasources/uid/"+uid, nil)
	if err != nil {
		t.Errorf("delete Grafana data source: %v", err)
		return
	}
	if status != http.StatusOK {
		t.Errorf("delete Grafana data source: status=%d body=%s", status, body)
	}
}

func doGrafanaAPIRequest(method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(method, grafanaAPIBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth("admin", "admin")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	return resp.StatusCode, responseBody, err
}

func requirePrometheusMatrix(t *testing.T, body []byte) {
	t.Helper()
	var response promResponse
	require.NoError(t, json.Unmarshal(body, &response))
	require.Equal(t, "success", response.Status)
	var data promQueryData
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.Equal(t, "matrix", data.ResultType)
	var series []json.RawMessage
	require.NoError(t, json.Unmarshal(data.Result, &series))
	require.NotEmpty(t, series)
}
