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
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLifecycle_ReloadPreservesHCStatus(t *testing.T) {
	// Drop prior SIGHUP handlers so this test owns the only live receiver.
	signal.Reset(syscall.SIGHUP)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/configs/reload.yaml")

	const metricsAddr = "127.0.0.1:8531"
	waitForTrickster(t, metricsAddr)

	healthURL := "http://" + metricsAddr + "/trickster/health"
	requireTargetAvailable(t, healthURL, "prom1", 15*time.Second)

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP),
		"failed to send SIGHUP for in-process reload")

	time.Sleep(500 * time.Millisecond)
	requireTargetAvailable(t, healthURL, "prom1", 15*time.Second)
}

func requireTargetAvailable(t *testing.T, healthURL, name string, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := http.NewRequest(http.MethodGet, healthURL, nil)
		if !assert.NoError(collect, err) {
			return
		}
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) {
			return
		}
		var hs struct {
			Available []struct {
				Name string `json:"name"`
			} `json:"available"`
		}
		if !assert.NoError(collect, json.Unmarshal(b, &hs),
			"health payload was not JSON: %s", string(b)) {
			return
		}
		names := make([]string, 0, len(hs.Available))
		for _, a := range hs.Available {
			names = append(names, a.Name)
		}
		assert.Contains(collect, names, name,
			"expected %q in available=%v (body=%s)", name, names, strings.TrimSpace(string(b)))
	}, timeout, 250*time.Millisecond,
		"%q never became available at %s", name, healthURL)
}
