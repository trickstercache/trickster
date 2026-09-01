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

package aws

import (
	"errors"
	"testing"
	"time"

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func reportingSubscription(t *testing.T) *subscription {
	t.Helper()
	p, err := newProvider("test-report", fakeOptions("http://example.com"))
	require.NoError(t, err)
	return &subscription{p: p}
}

// Ordering and the cap are covered by TestSummarizeIsStableAndBounded in
// aws_test.go; these are the edges it does not reach.
func TestSummarizeEdges(t *testing.T) {
	require.Empty(t, summarize(nil))

	unsorted := []excluded{
		{instanceID: "i-c", reason: "no port"},
		{instanceID: "i-a", reason: "no port"},
		{instanceID: "i-b", reason: "no address"},
	}
	require.Equal(t, "i-a: no port; i-b: no address; i-c: no port",
		summarize(unsorted))
	require.Equal(t, "i-c", unsorted[0].instanceID,
		"summarize must not reorder its caller's slice")

	// ties on id are broken by reason, so the order is total
	require.Equal(t, "i-a: alpha; i-a: zebra", summarize([]excluded{
		{instanceID: "i-a", reason: "zebra"},
		{instanceID: "i-a", reason: "alpha"},
	}))
}

func TestReportSkippedIsSuppressedUntilTheSetChanges(t *testing.T) {
	s := reportingSubscription(t)
	first := []excluded{{instanceID: "i-1", reason: "no port"}}

	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged)

	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged,
		"an identical set stays suppressed")

	changed := append(first, excluded{instanceID: "i-2", reason: "no address"})
	s.reportSkipped(changed)
	require.Equal(t, summarize(changed), s.skippedLogged)

	s.reportSkipped(nil)
	require.Empty(t, s.skippedLogged)
	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged,
		"the same exclusion after a clean poll is news again")
}

func TestWarnAndClearWarnBracketAFailureStreak(t *testing.T) {
	s := reportingSubscription(t)

	s.clearWarn()
	require.False(t, s.failing, "recovering when never failing is a no-op")

	s.warn(errors.New("UnauthorizedOperation"))
	require.True(t, s.failing)
	s.warn(errors.New("still failing"))
	require.True(t, s.failing, "the streak continues rather than re-logging")

	s.clearWarn()
	require.False(t, s.failing)
	s.clearWarn()
	require.False(t, s.failing)
}

func TestOnPanicCountsARefreshError(t *testing.T) {
	s := reportingSubscription(t)
	counter := metrics.DiscoveryRefreshErrors.WithLabelValues("test-report", "aws")
	before := testutil.ToFloat64(counter)
	s.onPanic("synthetic panic", []byte("goroutine 1 [running]:\n"))
	require.Greater(t, testutil.ToFloat64(counter), before)
}

func TestIntervalAndTimeoutHonorConfiguration(t *testing.T) {
	require.Equal(t, do.DefaultHTTPInterval, intervalOf(&do.HTTPOptions{}))
	require.Equal(t, do.DefaultHTTPTimeout, timeoutOf(&do.HTTPOptions{}))

	o := &do.HTTPOptions{
		Interval: timeconv.Duration(75 * time.Second),
		Timeout:  timeconv.Duration(30 * time.Second),
	}
	require.Equal(t, 75*time.Second, intervalOf(o))
	require.Equal(t, 30*time.Second, timeoutOf(o))
}

// ECS tags are a conjunction: every listed tag must be present, so a task
// carrying only some of them is not a match.
func TestECSHasAllTags(t *testing.T) {
	tk := &ecsTask{Tags: []ecsTag{
		{Key: "role", Value: "prom"}, {Key: "env", Value: "prod"}}}

	require.True(t, tk.hasAllTags(nil), "no required tags matches everything")
	require.True(t, tk.hasAllTags([]string{"role"}))
	require.True(t, tk.hasAllTags([]string{"role", "env"}))
	require.False(t, tk.hasAllTags([]string{"role", "absent"}),
		"every listed tag must be present, not any")
	require.False(t, (&ecsTask{}).hasAllTags([]string{"role"}))
}
