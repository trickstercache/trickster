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
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestClickHouse tests ClickHouse-specific capabilities through Trickster.
// Requires: make developer-start && make developer-seed-data.
// Shares the Trickster instance started by TestPrometheus (same dev config, same ports).
func TestClickHouse(t *testing.T) {
	developerHarness().start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	t.Run("time series query", func(t *testing.T) {
		now := time.Now()
		weekAgo := now.Add(-7 * 24 * time.Hour)
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
				"GROUP BY t ORDER BY t FORMAT JSON",
			weekAgo.Unix(), now.Unix(),
		)
		params := url.Values{"query": {q}}
		u := "http://" + tricksterAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))

		var result struct {
			Meta []struct{ Name string } `json:"meta"`
			Data []json.RawMessage       `json:"data"`
			Rows int                     `json:"rows"`
		}
		require.NoError(t, json.Unmarshal(body, &result))
		require.NotEmpty(t, result.Data, "expected rows from ClickHouse trips table")
		t.Logf("clickhouse: %d rows returned", result.Rows)

		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		t.Logf("clickhouse: %s", resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", hdr["engine"])
	})

	t.Run("non-select proxied", func(t *testing.T) {
		params := url.Values{"query": {"SHOW TABLES"}}
		u := "http://" + tricksterAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.Contains(t, string(body), "trips", "SHOW TABLES should include trips table")
		t.Logf("clickhouse non-select: %s", string(body))
	})
}
