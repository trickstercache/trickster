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

package gcp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// reportingSubscription is a subscription with just the state the
// failure-reporting paths touch; they never reach the network.
func reportingSubscription(t *testing.T) *subscription {
	t.Helper()
	p, err := newProvider("test-report", fakeOptions("http://example.com"))
	require.NoError(t, err)
	return &subscription{p: p}
}

// The summary is bounded so that a wholesale mis-tagging cannot emit a
// megabyte of log, and stable so repeated identical exclusions can be
// suppressed rather than logged every poll forever.
func TestSummarizeIsStableAndBounded(t *testing.T) {
	require.Empty(t, summarize(nil))

	unsorted := []excluded{
		{name: "vm-c", reason: "no port"},
		{name: "vm-a", reason: "no port"},
		{name: "vm-b", reason: "no address"},
	}
	got := summarize(unsorted)
	require.Equal(t, "vm-a: no port; vm-b: no address; vm-c: no port", got)

	// order in must not change the summary out, or suppression breaks
	reordered := []excluded{unsorted[1], unsorted[2], unsorted[0]}
	require.Equal(t, got, summarize(reordered))

	// summarize must not reorder its caller's slice
	require.Equal(t, "vm-c", unsorted[0].name)
}

func TestSummarizeSortsByReasonWhenNamesMatch(t *testing.T) {
	got := summarize([]excluded{
		{name: "vm-a", reason: "zebra"},
		{name: "vm-a", reason: "alpha"},
	})
	require.Equal(t, "vm-a: alpha; vm-a: zebra", got)
}

func TestSummarizeCapsTheList(t *testing.T) {
	many := make([]excluded, 0, maxSummarized+5)
	for i := range maxSummarized + 5 {
		many = append(many, excluded{
			name:   fmt.Sprintf("vm-%02d", i),
			reason: "no port",
		})
	}
	got := summarize(many)
	require.Equal(t, maxSummarized, strings.Count(got, "no port"),
		"exactly the cap is named")
	require.Contains(t, got, "; and 5 more")
	require.NotContains(t, got, "vm-14", "past the cap is elided, not listed")
}

// A permanently mis-tagged instance should be reported once, not on every
// poll forever; a change in the set must report again.
func TestReportSkippedIsSuppressedUntilTheSetChanges(t *testing.T) {
	s := reportingSubscription(t)
	first := []excluded{{name: "vm-1", reason: "no port"}}

	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged)

	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged,
		"an identical set stays suppressed")

	changed := append(first, excluded{name: "vm-2", reason: "no address"})
	s.reportSkipped(changed)
	require.Equal(t, summarize(changed), s.skippedLogged)

	// everything mappable again: the memo clears, so a recurrence later is
	// reported rather than silently suppressed
	s.reportSkipped(nil)
	require.Empty(t, s.skippedLogged)

	s.reportSkipped(first)
	require.Equal(t, summarize(first), s.skippedLogged,
		"the same exclusion after a clean poll is news again")
}

// A failure streak is logged once, and the recovery once.
func TestWarnAndClearWarnBracketAFailureStreak(t *testing.T) {
	s := reportingSubscription(t)
	require.False(t, s.failing)

	s.clearWarn()
	require.False(t, s.failing, "recovering when never failing is a no-op")

	s.warn(errors.New("boom"))
	require.True(t, s.failing)
	s.warn(errors.New("boom again"))
	require.True(t, s.failing, "the streak continues rather than re-logging")

	s.clearWarn()
	require.False(t, s.failing)
	s.clearWarn()
	require.False(t, s.failing, "recovering twice is a no-op")
}

// A panicking poll must surface on the same metric and log stream as an
// upstream failure, rather than silently freezing the membership.
func TestOnPanicCountsARefreshError(t *testing.T) {
	s := reportingSubscription(t)
	counter := metrics.DiscoveryRefreshErrors.WithLabelValues("test-report", "gcp")
	before := testutil.ToFloat64(counter)
	s.onPanic("synthetic panic", []byte("goroutine 1 [running]:\n"))
	require.Greater(t, testutil.ToFloat64(counter), before)
}

// The configured values must actually be used; a default that silently wins
// would make http.interval and http.timeout inert.
func TestIntervalAndTimeoutHonorConfiguration(t *testing.T) {
	require.Equal(t, do.DefaultHTTPInterval, intervalOf(&do.HTTPOptions{}))
	require.Equal(t, do.DefaultHTTPTimeout, timeoutOf(&do.HTTPOptions{}))

	o := &do.HTTPOptions{
		Interval: timeconv.Duration(90 * time.Second),
		Timeout:  timeconv.Duration(45 * time.Second),
	}
	require.Equal(t, 90*time.Second, intervalOf(o))
	require.Equal(t, 45*time.Second, timeoutOf(o))
}
