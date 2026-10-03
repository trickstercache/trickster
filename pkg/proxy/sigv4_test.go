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

package proxy

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestSigV4ObserverCountsEvents(t *testing.T) {
	const name = "sigv4-observer-test"
	obs := sigV4Observer(name)
	failures := metrics.ProxySigV4Events.WithLabelValues(name, sigV4EventSignFailure)
	retries := metrics.ProxySigV4Events.WithLabelValues(name, sigV4EventCredentialRetry)
	before := testutil.ToFloat64(failures)
	obs.SignFailed(errors.New("no region"))
	obs.SignFailed(errors.New("no region"))
	obs.CredentialsRetried("ExpiredTokenException")
	require.Equal(t, before+2, testutil.ToFloat64(failures),
		"every failure is counted even when its warning is throttled")
	require.Equal(t, float64(1), testutil.ToFloat64(retries))
}

func TestThrottledWarn(t *testing.T) {
	var logged int
	warn := throttledWarnWith(func(string, logging.Pairs) { logged++ })
	warn("a", nil)
	warn("a", nil)
	require.Equal(t, 1, logged)
}
