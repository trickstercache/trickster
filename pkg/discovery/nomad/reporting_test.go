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

package nomad

import (
	"testing"
	"time"

	nomadopts "github.com/trickstercache/trickster/v2/pkg/discovery/nomad/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A panicking poll must surface on the same metric and log stream as an
// upstream failure, rather than silently freezing the membership.
func TestOnPanicCountsARefreshError(t *testing.T) {
	p := &provider{name: "test-report", nomad: &nomadopts.Options{}}
	s := &subscription{p: p}

	counter := metrics.DiscoveryRefreshErrors.WithLabelValues("test-report", "nomad")
	before := testutil.ToFloat64(counter)
	s.onPanic("synthetic panic", []byte("goroutine 1 [running]:\n"))
	require.Greater(t, testutil.ToFloat64(counter), before)
}

// The configured cadence must actually be used; a default that silently
// won would make http.interval inert.
func TestIntervalHonorsConfiguration(t *testing.T) {
	p := &provider{http: &do.HTTPOptions{}, nomad: &nomadopts.Options{}}
	require.Equal(t, do.DefaultHTTPInterval, p.interval())

	p.http.Interval = timeconv.Duration(90 * time.Second)
	require.Equal(t, 90*time.Second, p.interval())
}

// The poll timeout must outlast the blocking-query wait, so when it is not
// configured it is derived from the wait rather than defaulted flat.
func TestTimeoutDerivesFromTheWaitWhenUnset(t *testing.T) {
	p := &provider{
		http:  &do.HTTPOptions{},
		nomad: &nomadopts.Options{Wait: timeconv.Duration(time.Minute)},
	}
	require.Equal(t, nomadopts.PollTimeout(time.Minute), p.timeout())
	require.Greater(t, p.timeout(), time.Minute,
		"a timeout that does not outlast the wait would abort every long poll")

	// an explicit timeout wins over the derived one
	p.http.Timeout = timeconv.Duration(3 * time.Minute)
	require.Equal(t, 3*time.Minute, p.timeout())
}
