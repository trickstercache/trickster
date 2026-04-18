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
	"testing"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

func newPromSDKClient(t *testing.T, address, backend string) v1.API {
	t.Helper()
	cfg := api.Config{Address: "http://" + address + "/" + backend}
	client, err := api.NewClient(cfg)
	require.NoError(t, err)
	return v1.NewAPI(client)
}

func TestPrometheusSDK(t *testing.T) {
	h := developerHarness()
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	sdk := newPromSDKClient(t, h.BaseAddr, "prom1")
	ctx := context.Background()

	t.Run("range_query", func(t *testing.T) {
		now := time.Now()
		step := 15 * time.Second
		end := now.Truncate(step)
		start := end.Add(-5 * time.Minute)
		result, warnings, err := sdk.QueryRange(ctx, "up", v1.Range{
			Start: start,
			End:   end,
			Step:  step,
		})
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		mat, ok := result.(model.Matrix)
		require.True(t, ok, "expected model.Matrix, got %T", result)
		require.Greater(t, len(mat), 0)
	})

	t.Run("instant_query", func(t *testing.T) {
		result, warnings, err := sdk.Query(ctx, "up", time.Now())
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		vec, ok := result.(model.Vector)
		require.True(t, ok, "expected model.Vector, got %T", result)
		require.Greater(t, len(vec), 0)
	})

	t.Run("labels", func(t *testing.T) {
		labels, warnings, err := sdk.LabelNames(ctx, nil, time.Now().Add(-5*time.Minute), time.Now())
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		require.Contains(t, labels, "__name__")
		require.Contains(t, labels, "job")
		require.Contains(t, labels, "instance")
	})

	t.Run("label_values", func(t *testing.T) {
		values, warnings, err := sdk.LabelValues(ctx, "job", nil, time.Now().Add(-5*time.Minute), time.Now())
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		found := false
		for _, v := range values {
			if string(v) == "prometheus" {
				found = true
				break
			}
		}
		require.True(t, found, "expected label value 'prometheus' in %v", values)
	})

	t.Run("series", func(t *testing.T) {
		now := time.Now()
		series, warnings, err := sdk.Series(ctx, []string{"up"}, now.Add(-5*time.Minute), now)
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		require.NotEmpty(t, series)
	})

	t.Run("metadata", func(t *testing.T) {
		meta, err := sdk.Metadata(ctx, "prometheus_http_request_duration_seconds", "")
		require.NoError(t, err)
		md, ok := meta["prometheus_http_request_duration_seconds"]
		require.True(t, ok, "expected metadata for prometheus_http_request_duration_seconds")
		require.NotEmpty(t, md)
		require.Equal(t, v1.MetricTypeHistogram, md[0].Type)
	})

	t.Run("targets", func(t *testing.T) {
		targets, err := sdk.Targets(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, targets.Active, "expected at least one active target")
	})

	t.Run("rules", func(t *testing.T) {
		rules, err := sdk.Rules(ctx)
		require.NoError(t, err)
		t.Logf("rule groups: %d", len(rules.Groups))
	})

	t.Run("alertmanagers", func(t *testing.T) {
		am, err := sdk.AlertManagers(ctx)
		require.NoError(t, err)
		t.Logf("active alertmanagers: %d, dropped: %d", len(am.Active), len(am.Dropped))
	})

	t.Run("cache_hit", func(t *testing.T) {
		now := time.Now()
		step := 15 * time.Second
		end := now.Truncate(step)
		start := end.Add(-5 * time.Minute)
		r := v1.Range{Start: start, End: end, Step: step}

		result1, _, err := sdk.QueryRange(ctx, "process_cpu_seconds_total", r)
		require.NoError(t, err)
		mat1, ok := result1.(model.Matrix)
		require.True(t, ok, "expected model.Matrix, got %T", result1)
		require.Greater(t, len(mat1), 0)

		result2, _, err := sdk.QueryRange(ctx, "process_cpu_seconds_total", r)
		require.NoError(t, err)
		mat2, ok := result2.(model.Matrix)
		require.True(t, ok, "expected model.Matrix from cache, got %T", result2)
		require.Greater(t, len(mat2), 0)
	})
}

func TestPrometheusSDK_ALB(t *testing.T) {
	h := albHarness()
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	sdk := newPromSDKClient(t, h.BaseAddr, "alb-tsm")
	ctx := context.Background()

	t.Run("tsm_range_query", func(t *testing.T) {
		now := time.Now()
		step := 15 * time.Second
		end := now.Truncate(step)
		start := end.Add(-5 * time.Minute)
		result, warnings, err := sdk.QueryRange(ctx, "up", v1.Range{
			Start: start,
			End:   end,
			Step:  step,
		})
		require.NoError(t, err)
		t.Logf("warnings: %v", warnings)
		mat, ok := result.(model.Matrix)
		require.True(t, ok, "expected model.Matrix, got %T", result)
		require.Greater(t, len(mat), 0)
	})
}
