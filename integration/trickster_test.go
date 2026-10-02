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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrickster(t *testing.T) {
	t.Run("config not found", func(t *testing.T) {
		ctx := context.Background()
		expected := expectedStartError{
			ErrorContains: new("open testdata/cfg-notfound.yaml: no such file or directory"),
		}
		startTrickster(t, ctx, expected, "-config", "testdata/cfg-notfound.yaml")
	})

	h := configHarness(t)
	h.start(t)

	t.Run("start and stop", func(t *testing.T) {
		metrics := checkTricksterMetrics(t, h.MetricsAddr)
		t.Log("Trickster metrics count:", len(metrics))
	})

	t.Run("health endpoint", func(t *testing.T) {
		waitForTrickster(t, h.MetricsAddr, "/trickster/health")

		// a backend is listed as available only once its first probe passes
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			req, err := http.NewRequest("GET", "http://"+h.MetricsAddr+"/trickster/health", nil)
			if !assert.NoError(collect, err) {
				return
			}
			req.Header.Set("Accept", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if !assert.NoError(collect, err) {
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if !assert.NoError(collect, err) || !assert.Equal(collect, http.StatusOK, resp.StatusCode) {
				return
			}
			var health struct {
				Title       string                  `json:"title"`
				Available   []struct{ Name string } `json:"available"`
				Unavailable []struct{ Name string } `json:"unavailable"`
			}
			if !assert.NoError(collect, json.Unmarshal(body, &health)) {
				return
			}
			assert.Equal(collect, "Trickster Backend Health Status", health.Title)
			assert.NotEmpty(collect, health.Available, "expected at least one available backend: %s", body)
		}, 15*time.Second, 100*time.Millisecond)
	})
}
