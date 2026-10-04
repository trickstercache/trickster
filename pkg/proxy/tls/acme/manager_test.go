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
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"

	"github.com/caddyserver/certmagic"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestManagerIssuesAndServes(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	m, path := newTestManager(t, sink, nil, stub)
	orders := testutil.ToFloat64(metrics.ACMEOrdersTotal.WithLabelValues(testIssuer, resultSuccess))
	require.NoError(t, m.Apply(testConfig(t, path, testDomain, testDomain2)))
	requireServed(t, sink, testDomain, testDomain2)
	require.Equal(t, int64(2), stub.issued.Load())
	require.Equal(t, map[string]string{testDomain: testIssuer, testDomain2: testIssuer}, m.Domains())
	require.Equal(t, orders+2, testutil.ToFloat64(metrics.ACMEOrdersTotal.WithLabelValues(testIssuer, resultSuccess)))

	// an unchanged configuration reuses its issuer and orders nothing
	before := m.issuers[testIssuer]
	require.NoError(t, m.Apply(testConfig(t, path, testDomain, testDomain2)))
	require.Same(t, before, m.issuers[testIssuer])
	require.Equal(t, int64(2), stub.issued.Load())
}

func TestManagerReconcilesDomains(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	m, path := newTestManager(t, sink, nil, stub)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain, testDomain2)))
	requireServed(t, sink, testDomain, testDomain2)

	// a removed domain leaves the listener; the one that remains keeps its certificate
	served := sink.served(testListener)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain)))
	require.Eventually(t, func() bool {
		now := sink.served(testListener)
		_, gone := now[testDomain2]
		return !gone && now[testDomain] == served[testDomain]
	}, testWait, testPoll)

	// an issuer change keeps serving the stored certificate while the new issuer takes over
	conf := testConfig(t, path, testDomain)
	conf.ACME.Issuers[testIssuer].Email = "other@trickstercache.org"
	old := m.issuers[testIssuer]
	require.NoError(t, m.Apply(conf))
	require.NotSame(t, old, m.issuers[testIssuer])
	require.Equal(t, served[testDomain], requireServed(t, sink, testDomain)[testDomain])
	require.Eventually(t, func() bool {
		certs := m.epoch.cache.AllMatchingCertificates(testDomain)
		return len(certs) == 1 && certs[0].Leaf.SerialNumber.String() == served[testDomain]
	}, testWait, testPoll, "the replacement issuer never cached the stored certificate")
	require.Equal(t, int64(2), stub.issued.Load())
}

func TestManagerDisableWithdrawsCertificates(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	m, path := newTestManager(t, sink, nil, stub)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain)))
	requireServed(t, sink, testDomain)
	conf := config.NewConfig()
	require.NoError(t, m.Apply(conf))
	require.False(t, sink.has(testListener))
	require.Nil(t, m.Domains())
	require.Nil(t, m.table.Load())
	require.ErrorIs(t, m.Renew(testDomain), ErrUnmanagedDomain)
}

func TestManagerRestartLoadsFromStorage(t *testing.T) {
	stub := newStubIssuer(t)
	first := newRecordingSink()
	m, path := newTestManager(t, first, nil, stub)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain)))
	serial := requireServed(t, first, testDomain)[testDomain]
	m.Close()

	second := newRecordingSink()
	m2, _ := newTestManager(t, second, nil, stub)
	require.NoError(t, m2.Apply(testConfig(t, path, testDomain)))
	require.Equal(t, serial, requireServed(t, second, testDomain)[testDomain])
	require.Equal(t, int64(1), stub.issued.Load())
}

func TestManagerRenew(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	m, path := newTestManager(t, sink, nil, stub)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain)))
	serial := requireServed(t, sink, testDomain)[testDomain]
	require.NoError(t, m.Renew("WWW.acme.test."))
	require.Eventually(t, func() bool {
		return sink.served(testListener)[testDomain] != serial
	}, testWait, testPoll)
	require.ErrorIs(t, m.Renew("unknown.acme.test"), ErrUnmanagedDomain)
	require.Error(t, m.Renew("*.*.bad"))
}

func TestManagerStartupWait(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	state := &ready.State{}
	state.SetCertsPending()
	m, path := newTestManager(t, sink, state, stub)
	stub.Hold()
	t.Cleanup(stub.Release)
	conf := testConfig(t, path, testDomain)
	conf.ACME.WaitOnStartup = timeconv.Duration(testWait)
	require.NoError(t, m.Apply(conf))
	require.True(t, state.CertsPending())
	stub.Release()
	require.Eventually(t, func() bool { return !state.CertsPending() }, testWait, testPoll)
}

func TestManagerStartupWaitTimesOut(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	stub.fail.Store(true)
	state := &ready.State{}
	m, path := newTestManager(t, sink, state, stub)
	timeouts := testutil.ToFloat64(metrics.ACMEStartupWaitTimeoutsTotal)
	failures := testutil.ToFloat64(metrics.ACMEOrdersTotal.WithLabelValues(testIssuer, resultFailure))
	conf := testConfig(t, path, testDomain)
	conf.ACME.WaitOnStartup = timeconv.Duration(500 * time.Millisecond)
	require.NoError(t, m.Apply(conf))
	require.True(t, state.CertsPending())
	require.Eventually(t, func() bool { return !state.CertsPending() }, testWait, testPoll)
	require.Equal(t, timeouts+1, testutil.ToFloat64(metrics.ACMEStartupWaitTimeoutsTotal))
	require.Greater(t, testutil.ToFloat64(metrics.ACMEOrdersTotal.WithLabelValues(testIssuer, resultFailure)), failures)
	require.Equal(t, []string{testDomain}, m.missing([]string{testDomain}))
}

func TestManagerStartupWithoutWaitReleasesReadiness(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		wait    time.Duration
		enabled bool
	}{
		{name: "no wait", domains: []string{testDomain}, enabled: true},
		{name: "no domains", wait: time.Minute, enabled: true},
		{name: "disabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &ready.State{}
			state.SetCertsPending()
			sink := newRecordingSink()
			m, path := newTestManager(t, sink, state, newStubIssuer(t))
			conf := config.NewConfig()
			if test.enabled {
				conf = testConfig(t, path, test.domains...)
				conf.ACME.WaitOnStartup = timeconv.Duration(test.wait)
			}
			require.NoError(t, m.Apply(conf))
			require.False(t, state.CertsPending())
			if test.enabled && len(test.domains) > 0 {
				// issuance continues after readiness; a job canceled mid-obtain by cleanup can
				// still be writing to the storage directory as it is removed
				requireServed(t, sink, test.domains...)
			}
		})
	}
}

func TestManagerApplyErrors(t *testing.T) {
	state := &ready.State{}
	state.SetCertsPending()
	path := t.TempDir()
	m := New(newRecordingSink(), state)
	t.Cleanup(m.Close)
	m.factory = func(*certmagic.Config, *acmeopts.IssuerOptions, issuerPorts, *zap.Logger,
	) (certmagic.Issuer, *certmagic.ACMEIssuer, error) {
		return nil, nil, errStubRefused
	}
	require.ErrorIs(t, m.Apply(testConfig(t, path, testDomain)), errStubRefused)
	require.False(t, state.CertsPending(), "a failed first apply must release readiness")

	conf := testConfig(t, "", testDomain)
	conf.ACME.Storage = nil
	m2 := New(newRecordingSink(), nil)
	require.ErrorIs(t, m2.Apply(conf), acmeopts.ErrStoragePathRequired)
}

func TestManagerSinkRetriesUnknownListener(t *testing.T) {
	stub, sink := newStubIssuer(t), newRecordingSink()
	sink.refuse.Store(true)
	m, path := newTestManager(t, sink, nil, stub)
	require.NoError(t, m.Apply(testConfig(t, path, testDomain)))
	require.Eventually(t, func() bool { return stub.issued.Load() == 1 }, testWait, testPoll)
	sink.refuse.Store(false)
	m.kickSync()
	requireServed(t, sink, testDomain)
}

func TestConfigForCert(t *testing.T) {
	m := New(newRecordingSink(), nil)
	_, err := m.configForCert(certmagic.Certificate{Names: []string{testDomain}})
	require.Error(t, err)
	cfg := &certmagic.Config{}
	m.table.Store(&table{configs: map[string]*certmagic.Config{testDomain: cfg}})
	got, err := m.configForCert(certmagic.Certificate{Names: []string{testDomain}})
	require.NoError(t, err)
	require.Same(t, cfg, got)
	odCfg := &certmagic.Config{}
	m.table.Store(&table{onDemand: &onDemand{issuer: &issuer{cfg: odCfg}}})
	got, err = m.configForCert(certmagic.Certificate{Names: []string{"other.acme.test"}})
	require.NoError(t, err)
	require.Same(t, odCfg, got)
}

func TestSolversWithoutState(t *testing.T) {
	m := New(newRecordingSink(), nil)
	require.False(t, m.ServeHTTPChallenge(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)))
	_, err := m.TLSALPNCertificate(&tls.ClientHelloInfo{ServerName: testDomain})
	require.ErrorIs(t, err, ErrUnmanagedDomain)
	require.False(t, m.Enabled("listener.default.https"))
	_, err = m.Certificate(&tls.ClientHelloInfo{ServerName: testDomain})
	require.ErrorIs(t, err, errOnDemandOff)
	require.ErrorIs(t, m.decide(t.Context(), testDomain), errOnDemandOff)

	// issuers without an ACME client or the challenge are skipped
	m.table.Store(&table{issuers: []*issuer{{opts: &acmeopts.IssuerOptions{}}}})
	require.False(t, m.ServeHTTPChallenge(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)))
	_, err = m.TLSALPNCertificate(&tls.ClientHelloInfo{ServerName: testDomain})
	require.ErrorIs(t, err, ErrUnmanagedDomain)
}

func TestOnEventIgnoresIncompleteData(t *testing.T) {
	m := New(newRecordingSink(), nil)
	fn := m.onEvent(testIssuer)
	require.NoError(t, fn(t.Context(), eventCertObtained, map[string]any{dataIdentifier: testDomain}))
	require.NoError(t, fn(t.Context(), eventCertFailed, map[string]any{
		dataIdentifier: testDomain,
		dataError:      errors.New("boom"), dataRenewal: true,
	}))
	require.Equal(t, "boom", m.failures[testDomain])
	require.NoError(t, fn(t.Context(), eventCachedCert, nil))
	require.True(t, shouldEmit(eventCachedCert))
	require.False(t, shouldEmit("tls_get_certificate"))
}

func TestCloseNilManager(t *testing.T) {
	var m *Manager
	m.Close()
}
