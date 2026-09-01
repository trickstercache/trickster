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

package azure

import (
	"errors"
	"net/http"
	"testing"
	"time"

	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func reportingSubscription(t *testing.T) *subscription {
	t.Helper()
	p, err := newProvider("test-report", fakeOptions("https://example.com"))
	require.NoError(t, err)
	return &subscription{p: p}
}

// A permanently unmappable VM should be reported once, not on every poll
// forever; a change in the set must report again.
func TestReportSkippedIsSuppressedUntilTheSetChanges(t *testing.T) {
	s := reportingSubscription(t)
	first := []excluded{{name: "web-1", reason: "vm has no network interface"}}

	s.reportSkipped(first)
	memo := s.skippedLogged
	require.NotEmpty(t, memo)

	s.reportSkipped(first)
	require.Equal(t, memo, s.skippedLogged, "an identical set stays suppressed")

	s.reportSkipped(append(first,
		excluded{name: "web-2", reason: "vm has no private ip address"}))
	require.NotEqual(t, memo, s.skippedLogged)
	require.Contains(t, s.skippedLogged, "web-1")
	require.Contains(t, s.skippedLogged, "web-2")

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

	s.warn(errors.New("AuthorizationFailed"))
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
	counter := metrics.DiscoveryRefreshErrors.WithLabelValues("test-report", "azure")
	before := testutil.ToFloat64(counter)
	s.onPanic("synthetic panic", []byte("goroutine 1 [running]:\n"))
	require.Greater(t, testutil.ToFloat64(counter), before)
}

func TestIntervalAndTimeoutHonorConfiguration(t *testing.T) {
	require.Equal(t, do.DefaultHTTPInterval, intervalOf(&do.HTTPOptions{}))
	require.Equal(t, do.DefaultHTTPTimeout, timeoutOf(&do.HTTPOptions{}))

	o := &do.HTTPOptions{
		Interval: timeconv.Duration(120 * time.Second),
		Timeout:  timeconv.Duration(20 * time.Second),
	}
	require.Equal(t, 120*time.Second, intervalOf(o))
	require.Equal(t, 20*time.Second, timeoutOf(o))
}

// --- managed identity ------------------------------------------------------

// imdsSourceFor points the managed-identity flow at a fake, which is what
// the imdsURL field exists for.
func imdsSourceFor(o *azureopts.Options, url string) *tokenSource {
	ts := newTokenSource(o, http.DefaultClient)
	ts.imdsURL = url
	return ts
}

// The system-assigned identity of the host: no credential fields at all,
// and the Metadata header IMDS requires.
func TestManagedIdentityRequestShape(t *testing.T) {
	f := newFakeLogin(t, goodToken(`"3599"`))
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)

	tok, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "tok-abc", tok)

	form := f.nextForm(t)
	require.Equal(t, imdsAPIVersion, form.Get("api-version"))
	require.Equal(t, "https://management.azure.com/", form.Get("resource"),
		"IMDS wants the resource with a trailing slash, not the .default scope")
	require.Empty(t, form.Get("client_id"),
		"no client_id selects the system-assigned identity")
}

// A user-assigned identity is selected by client_id alone -- with no
// secret, so it must not be mistaken for a half-specified credential.
func TestManagedIdentitySelectsAUserAssignedIdentity(t *testing.T) {
	f := newFakeLogin(t, goodToken(`"3599"`))
	ts := imdsSourceFor(&azureopts.Options{ClientID: "user-assigned-1"}, f.URL)

	_, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "user-assigned-1", f.nextForm(t).Get("client_id"))
}

// IMDS sends expires_in as a quoted string; the token must still cache
// rather than being re-acquired on every call.
func TestManagedIdentityTokenIsCached(t *testing.T) {
	f := newFakeLogin(t, goodToken(`"3599"`))
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)

	for range 3 {
		_, err := ts.Token(t.Context())
		require.NoError(t, err)
	}
	require.Equal(t, 1, f.calls, "one acquisition serves every poll until expiry")
}

// An identity that is not assigned to the host is the common misconfiguration,
// and the failure must name IMDS rather than a login endpoint that was never
// contacted.
func TestManagedIdentityFailureNamesIMDS(t *testing.T) {
	f := newFakeLogin(t,
		`{"error":"invalid_request","error_description":"Identity not found"}`)
	f.status = http.StatusBadRequest
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)

	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "instance metadata service")
	require.Contains(t, err.Error(), "Identity not found")
	require.NotContains(t, err.Error(), "entra id")
}

// A response that is not a token document at all must not read as success.
func TestTokenResponseWithoutATokenIsAnError(t *testing.T) {
	f := newFakeLogin(t, `{"token_type":"Bearer"}`)
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "carried no token")
}

func TestUnparseableTokenResponseIsAnError(t *testing.T) {
	f := newFakeLogin(t, `<html>proxy error</html>`)
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not parse")
}

// A non-2xx with a body that parses but carries no error field still fails,
// rather than being read as an empty success.
func TestNon2xxWithoutAnErrorFieldStillFails(t *testing.T) {
	f := newFakeLogin(t, `{}`)
	f.status = http.StatusInternalServerError
	ts := imdsSourceFor(&azureopts.Options{}, f.URL)
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestTokenRequestTransportFailureIsReported(t *testing.T) {
	ts := imdsSourceFor(&azureopts.Options{}, "http://127.0.0.1:1/token")
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "instance metadata service")
}
