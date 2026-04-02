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
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test Trickster capabilities common to all backends / caches / configurations.
func TestTrickster(t *testing.T) {
	t.Run("config not found", func(t *testing.T) {
		// Simple test to ensure trickster returns an error if its config is not found.
		ctx := context.Background()
		expected := expectedStartError{
			ErrorContains: new("open testdata/cfg-notfound.yaml: no such file or directory"),
		}
		startTrickster(t, ctx, expected, "-config", "testdata/cfg-notfound.yaml")
	})
	t.Run("start and stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go startTrickster(t, ctx, expectedStartError{}, "-config", "../docs/developer/environment/trickster-config/trickster.yaml")
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			resp, err := http.Get("http://localhost:8481/metrics")
			if !assert.NoError(collect, err) {
				return
			}
			resp.Body.Close()
			assert.Equal(collect, 200, resp.StatusCode)
		}, 10*time.Second, 250*time.Millisecond, "trickster did not become ready")
		metrics := checkTricksterMetrics(t, "localhost:8481")
		t.Log("Trickster metrics count:", len(metrics))
	})
}

func new(s string) *string { return &s }
