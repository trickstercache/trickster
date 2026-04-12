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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test Trickster capabilities common to all backends / caches / configurations.
func TestTrickster(t *testing.T) {
	t.Run("config not found", func(t *testing.T) {
		ctx := context.Background()
		expected := expectedStartError{
			ErrorContains: new("open testdata/cfg-notfound.yaml: no such file or directory"),
		}
		startTrickster(t, ctx, expected, "-config", "testdata/cfg-notfound.yaml")
	})

	// Boot once for both start-and-stop and health subtests.
	cfg := writeTestConfig(t, 8578, 8579, 8586)
	h := tricksterHarness{
		ConfigPath:  cfg,
		BaseAddr:    "127.0.0.1:8578",
		MetricsAddr: "127.0.0.1:8579",
	}
	h.start(t)

	t.Run("start and stop", func(t *testing.T) {
		metrics := checkTricksterMetrics(t, "127.0.0.1:8579")
		t.Log("Trickster metrics count:", len(metrics))
	})

	t.Run("health endpoint", func(t *testing.T) {
		waitForTrickster(t, "127.0.0.1:8579", "/trickster/health")

		req, err := http.NewRequest("GET", "http://127.0.0.1:8579/trickster/health", nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("health response: %s", string(body))

		var health struct {
			Title       string `json:"title"`
			Available   []struct{ Name string } `json:"available"`
			Unavailable []struct{ Name string } `json:"unavailable"`
		}
		require.NoError(t, json.Unmarshal(body, &health))
		require.Equal(t, "Trickster Backend Health Status", health.Title)
		require.NotEmpty(t, health.Available, "expected at least one available backend")
	})
}
