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

package acme

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"

	"github.com/caddyserver/certmagic"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	testOnDemandKey    = "listener.default.https"
	testOnDemandName   = "shop.customers.acme.test"
	testOnDemandAsked  = "asked.acme.test"
	testOnDemandOutset = "elsewhere.example.test"
)

func onDemandTestConfig(t *testing.T, path string, od *acmeopts.OnDemandOptions) *config.Config {
	t.Helper()
	conf := testConfig(t, path)
	conf.Backends[testBackend].TLS = nil
	conf.ACME.OnDemand = od
	conf.ACME.Initialize()
	return conf
}

func decisions(result string) float64 {
	return testutil.ToFloat64(metrics.ACMEOnDemandDecisionsTotal.WithLabelValues(result))
}

func TestOnDemandIssuesAllowedNames(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	m, path := newTestManager(t, sink, nil, stub)
	conf := onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{
		Issuer:    testIssuer,
		Listeners: []string{testListener}, AllowedDomains: []string{"*.customers.acme.test"},
	})
	require.NoError(t, m.Apply(conf))
	require.True(t, m.Enabled(testOnDemandKey))
	require.False(t, m.Enabled("listener.other.https"))

	cert, err := m.Certificate(&tls.ClientHelloInfo{ServerName: testOnDemandName})
	require.NoError(t, err)
	require.Equal(t, []string{testOnDemandName}, cert.Leaf.DNSNames)
	requireServed(t, sink, testOnDemandName)

	refused := decisions(decisionRefused)
	_, err = m.Certificate(&tls.ClientHelloInfo{ServerName: testOnDemandOutset})
	require.Error(t, err)
	require.Equal(t, refused+1, decisions(decisionRefused))
	held := decisions(decisionRefusedHeld)
	_, err = m.Certificate(&tls.ClientHelloInfo{ServerName: testOnDemandOutset})
	require.Error(t, err)
	require.Equal(t, held+1, decisions(decisionRefusedHeld))

	// an unchanged reload keeps the decision state, and dropping on-demand withdraws its names
	od := m.od
	require.NoError(t, m.Apply(conf))
	require.Same(t, od.refused, m.od.refused)
	require.NoError(t, m.Apply(testConfig(t, path)))
	require.False(t, m.Enabled(testOnDemandKey))
	require.Eventually(t, func() bool { return !sink.has(testListener) }, testWait, testPoll)
}

func TestOnDemandAsk(t *testing.T) {
	ask := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(askParam) == testOnDemandAsked {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(ask.Close)
	m, path := newTestManager(t, newRecordingSink(), nil, newStubIssuer(t))
	require.NoError(t, m.Apply(onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{
		Issuer: testIssuer, Listeners: []string{testListener}, Ask: ask.URL + "/check?x=1", RateLimit: 1,
	})))
	ctx := t.Context()
	require.NoError(t, m.decide(ctx, testOnDemandAsked))
	require.ErrorIs(t, m.decide(ctx, testOnDemandOutset), errRefused)

	// the cached approval skips the ask, and the one-per-minute limiter refuses the second start
	limited := decisions(decisionRateLimited)
	require.ErrorIs(t, m.decide(ctx, testOnDemandAsked), errRateLimited)
	require.Equal(t, limited+1, decisions(decisionRateLimited))

	ask.Close()
	askErrors := decisions(decisionAskError)
	require.ErrorIs(t, m.decide(ctx, "unasked.acme.test"), errAskUnavailable)
	require.Equal(t, askErrors+1, decisions(decisionAskError))
}

func TestOnDemandAskURLErrors(t *testing.T) {
	od := newOnDemand(&acmeopts.OnDemandOptions{
		Ask: "http://[::1]:namedport", RateLimit: 1,
		DecisionTTL: acmeopts.DefaultOnDemandDecisionTTL,
	}, nil, nil)
	_, err := od.ask(t.Context(), testOnDemandName)
	require.Error(t, err)
	od.opts.Ask = "http://127.0.0.1/\x7f"
	_, err = od.ask(t.Context(), testOnDemandName)
	require.Error(t, err)
}

func TestAllowedDomain(t *testing.T) {
	allowed := []string{"exact.acme.test", "*.one.acme.test", "**.any.acme.test"}
	for name, want := range map[string]bool{
		"exact.acme.test":   true,
		"a.one.acme.test":   true,
		"a.b.one.acme.test": false,
		"a.b.any.acme.test": true,
		"one.acme.test":     false,
		"other.acme.test":   false,
	} {
		require.Equal(t, want, allowedDomain(allowed, name), name)
	}
}

func TestOnDemandIssuerError(t *testing.T) {
	stub := newStubIssuer(t)
	m, path := newTestManager(t, newRecordingSink(), nil, stub)
	conf := onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{
		Issuer:    testIssuer,
		Listeners: []string{testListener}, AllowedDomains: []string{testOnDemandName},
	})
	require.NoError(t, m.Apply(conf))
	m.factory = func(*certmagic.Config, *acmeopts.IssuerOptions, issuerPorts, *zap.Logger,
	) (certmagic.Issuer, *certmagic.ACMEIssuer, error) {
		return nil, nil, errStubRefused
	}
	changed := onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{
		Issuer:    testIssuer,
		Listeners: []string{testListener}, AllowedDomains: []string{"*.acme.test"},
	})
	require.ErrorIs(t, m.Apply(changed), errStubRefused)
	require.True(t, m.Enabled(testOnDemandKey), "a failed apply keeps the running on-demand state")
}

func TestFailedApplyKeepsRunningIssuers(t *testing.T) {
	stub := newStubIssuer(t)
	m, path := newTestManager(t, newRecordingSink(), nil, stub)
	require.NoError(t, m.Apply(onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{Issuer: testIssuer,
		Listeners: []string{testListener}, AllowedDomains: []string{testOnDemandName}})))
	running := m.issuers[testIssuer]
	calls := 0
	m.factory = func(*certmagic.Config, *acmeopts.IssuerOptions, issuerPorts, *zap.Logger,
	) (certmagic.Issuer, *certmagic.ACMEIssuer, error) {
		// the reconfigured static issuer builds, and the on-demand issuer built after it fails
		calls++
		if calls > 1 {
			return nil, nil, errStubRefused
		}
		return stub, nil, nil
	}
	changed := onDemandTestConfig(t, path, &acmeopts.OnDemandOptions{Issuer: testIssuer,
		Listeners: []string{testListener}, AllowedDomains: []string{"*.acme.test"}})
	changed.ACME.Issuers[testIssuer].Email = "changed@trickstercache.org"
	require.ErrorIs(t, m.Apply(changed), errStubRefused)
	require.Same(t, running, m.issuers[testIssuer])
	require.NoError(t, running.ctx.Err(), "a failed apply must not stop the running issuer")
	require.Len(t, m.table.Load().solvers(), 2)
}
