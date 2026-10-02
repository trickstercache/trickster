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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	"github.com/caddyserver/certmagic"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	testIssuer   = "stub"
	testListener = "default"
	testBackend  = "site"
	testDomain   = "www.acme.test"
	testDomain2  = "api.acme.test"
	testWait     = 10 * time.Second
	testPoll     = 10 * time.Millisecond
	testHTTPPort = 18480
	testTLSPort  = 18483
)

var errStubRefused = errors.New("stub issuer refused")

type stubIssuer struct {
	key    *ecdsa.PrivateKey
	ca     *x509.Certificate
	caDER  []byte
	issued atomic.Int64
	fail   atomic.Bool
	mtx    sync.Mutex
	hold   chan struct{}
}

func newStubIssuer(t *testing.T) *stubIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acme stub ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &stubIssuer{key: key, ca: ca, caDER: der}
}

func (s *stubIssuer) IssuerKey() string {
	return testIssuer
}

func (s *stubIssuer) Hold() {
	s.mtx.Lock()
	s.hold = make(chan struct{})
	s.mtx.Unlock()
}

func (s *stubIssuer) Release() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.hold != nil {
		close(s.hold)
		s.hold = nil
	}
}

func (s *stubIssuer) Issue(ctx context.Context, csr *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	s.mtx.Lock()
	hold := s.hold
	s.mtx.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.fail.Load() {
		return nil, errStubRefused
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, err
	}
	n := s.issued.Add(1)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: csr.DNSNames[0]},
		DNSNames:     csr.DNSNames,
		// each issuance is a second newer than the last, so renewals compare as newer
		NotBefore:   time.Now().Add(-time.Hour).Add(time.Duration(n) * time.Second),
		NotAfter:    time.Now().Add(23 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca, csr.PublicKey, s.key)
	if err != nil {
		return nil, err
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.caDER})...)
	return &certmagic.IssuedCertificate{Certificate: chain}, nil
}

func (s *stubIssuer) factory() issuerFactory {
	return func(*certmagic.Config, *acmeopts.IssuerOptions, issuerPorts, *zap.Logger,
	) (certmagic.Issuer, *certmagic.ACMEIssuer, error) {
		return s, nil, nil
	}
}

type recordingSink struct {
	mtx     sync.Mutex
	entries map[string][]*tr.Entry
	refuse  atomic.Bool
}

func newRecordingSink() *recordingSink {
	return &recordingSink{entries: make(map[string][]*tr.Entry)}
}

func (r *recordingSink) SetACMECerts(listenerName string, entries []*tr.Entry) error {
	if r.refuse.Load() {
		return errors.New("no such listener")
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if entries == nil {
		delete(r.entries, listenerName)
		return nil
	}
	r.entries[listenerName] = slices.Clone(entries)
	return nil
}

func (r *recordingSink) served(listenerName string) map[string]string {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	out := make(map[string]string)
	for _, e := range r.entries[listenerName] {
		for _, n := range e.Certificate.Leaf.DNSNames {
			out[n] = e.Certificate.Leaf.SerialNumber.String()
		}
	}
	return out
}

func (r *recordingSink) has(listenerName string) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	_, ok := r.entries[listenerName]
	return ok
}

func testConfig(t *testing.T, storagePath string, domains ...string) *config.Config {
	t.Helper()
	conf := config.NewConfig()
	conf.ACME = &acmeopts.Options{
		Storage: &acmeopts.StorageOptions{Path: storagePath},
		Issuers: map[string]*acmeopts.IssuerOptions{
			testIssuer: {AgreeToTerms: true, Email: "ops@trickstercache.org"},
		},
	}
	conf.ACME.Initialize()
	lo := listener.New(testListener)
	lo.ListenAddress, lo.TLSListenAddress = "127.0.0.1", "127.0.0.1"
	lo.ListenPort, lo.TLSListenPort, lo.ServeTLS = testHTTPPort, testTLSPort, true
	lo.Protocol = listener.ProtocolHTTP
	conf.Listeners[testListener] = lo
	b := bo.New()
	b.Name = testBackend
	b.Hosts = domains
	b.ListenerNames = []string{testListener}
	b.TLS = &to.Options{ACME: &acmeopts.BackendOptions{Issuer: testIssuer}}
	conf.Backends = bo.Lookup{testBackend: b}
	return conf
}

func newTestManager(t *testing.T, sink CertSink, readiness *ready.State, stub *stubIssuer) (*Manager, string) {
	// cleanups run in reverse, so the storage directory made first outlives the manager
	t.Helper()
	path := t.TempDir()
	m := New(sink, readiness)
	m.factory = stub.factory()
	t.Cleanup(m.Close)
	return m, path
}

func requireServed(t *testing.T, sink *recordingSink, domains ...string) map[string]string {
	t.Helper()
	var served map[string]string
	require.Eventually(t, func() bool {
		served = sink.served(testListener)
		for _, d := range domains {
			if _, ok := served[d]; !ok {
				return false
			}
		}
		return true
	}, testWait, testPoll, "domains never served: %v", domains)
	return served
}
