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

package docker

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

// A permanently unmappable container should be reported once, not on every
// poll forever; a change in the set must report again.
func TestReportSkippedIsSuppressedUntilTheSetChanges(t *testing.T) {
	s := reportingSubscription(t)
	first := []excluded{{name: "web-1", reason: "container exposes no tcp port"}}

	s.reportSkipped(first)
	require.NotEmpty(t, s.skippedLogged)
	memo := s.skippedLogged

	s.reportSkipped(first)
	require.Equal(t, memo, s.skippedLogged, "an identical set stays suppressed")

	s.reportSkipped(append(first,
		excluded{name: "web-2", reason: "container is attached to no network"}))
	require.NotEqual(t, memo, s.skippedLogged)
	require.Contains(t, s.skippedLogged, "web-1")
	require.Contains(t, s.skippedLogged, "web-2")
	require.Contains(t, s.skippedLogged, "; ", "entries are separated")

	// everything mappable again: the memo clears, so a recurrence later is
	// reported rather than silently suppressed
	s.reportSkipped(nil)
	require.Empty(t, s.skippedLogged)
	s.reportSkipped(first)
	require.Equal(t, memo, s.skippedLogged,
		"the same exclusion after a clean poll is news again")
}

func TestWarnAndClearWarnBracketAFailureStreak(t *testing.T) {
	s := reportingSubscription(t)

	s.clearWarn()
	require.False(t, s.failing, "recovering when never failing is a no-op")

	s.warn(errors.New("dial unix: no such file"))
	require.True(t, s.failing)
	s.warn(errors.New("still down"))
	require.True(t, s.failing, "the streak continues rather than re-logging")

	s.clearWarn()
	require.False(t, s.failing)
	s.clearWarn()
	require.False(t, s.failing)
}

// A panicking poll must surface as a refresh error rather than silently
// freezing the membership.
func TestOnPanicCountsARefreshError(t *testing.T) {
	s := reportingSubscription(t)
	counter := metrics.DiscoveryRefreshErrors.WithLabelValues("test-report", "docker")
	before := testutil.ToFloat64(counter)
	s.onPanic("synthetic panic", []byte("goroutine 1 [running]:\n"))
	require.Greater(t, testutil.ToFloat64(counter), before)
}

func TestIntervalAndTimeoutHonorConfiguration(t *testing.T) {
	require.Equal(t, do.DefaultHTTPInterval, intervalOf(&do.HTTPOptions{}))
	require.Equal(t, do.DefaultHTTPTimeout, timeoutOf(&do.HTTPOptions{}))

	o := &do.HTTPOptions{
		Interval: timeconv.Duration(15 * time.Second),
		Timeout:  timeconv.Duration(7 * time.Second),
	}
	require.Equal(t, 15*time.Second, intervalOf(o))
	require.Equal(t, 7*time.Second, timeoutOf(o))
}

// The exclusion messages name the address type and the network, so an
// operator can tell which knob to reach for.
func TestExclusionMessageHelpers(t *testing.T) {
	require.Equal(t, "IP", addressKind(""))
	require.Equal(t, "IP", addressKind(do.AddressPrivate))
	require.Equal(t, "IP", addressKind(do.AddressPublic))
	require.Equal(t, "global IPv6", addressKind(do.AddressIPv6))

	c := &container{}
	require.Equal(t, "backend", c.networkName("backend"))
	require.Equal(t, "(its only network)", c.networkName(""),
		"with no network configured the message must still read sensibly")
}

// A container id shorter than the abbreviation is returned whole rather
// than sliced out of range.
func TestShortIDHandlesShortInput(t *testing.T) {
	require.Equal(t, "abc", shortID("abc"))
	require.Equal(t, "", shortID(""))
	require.Equal(t, "0123456789ab", shortID("0123456789abcdef0123"))
	require.Len(t, shortID("0123456789abcdef0123"), 12)
}
