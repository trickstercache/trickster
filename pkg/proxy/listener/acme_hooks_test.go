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

package listener

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/challenge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/ondemand"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

const (
	hookTestKey      = "listener.hooks.https"
	hookChallengeSAN = "challenge.acme.test"
	hookOnDemandSAN  = "ondemand.acme.test"
)

func namedCert(t *testing.T, name string) *tls.Certificate {
	t.Helper()
	k, c, err := tlstest.GetTestKeyAndCertWithNames(name)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tr.ValidatePair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	return &cert
}

type hookSolver struct{ cert *tls.Certificate }

func (h hookSolver) ServeHTTPChallenge(http.ResponseWriter, *http.Request) bool { return false }

func (h hookSolver) TLSALPNCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return h.cert, nil
}

type hookProvider struct{ cert *tls.Certificate }

func (h hookProvider) Enabled(key string) bool { return key == hookTestKey }

func (h hookProvider) Certificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return h.cert, nil
}

func hookHandshake(t *testing.T, addr, serverName string, protos ...string) tls.ConnectionState {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test certs
		ServerName:         serverName,
		NextProtos:         protos,
	})
	if err != nil {
		t.Fatalf("handshake for %s: %v", serverName, err)
	}
	defer conn.Close()
	return conn.ConnectionState()
}

func TestTLSListenerACMEHooks(t *testing.T) {
	t.Cleanup(func() {
		challenge.SetSolver(nil)
		ondemand.SetProvider(nil)
	})
	challenge.SetSolver(hookSolver{cert: namedCert(t, hookChallengeSAN)})
	ondemand.SetProvider(hookProvider{cert: namedCert(t, hookOnDemandSAN)})
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(0) })
	go lg.StartListener(hookTestKey, "127.0.0.1", 0, 0, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2"},
	}, http.NotFoundHandler(), nil, nil, testLimits, nil)
	deadline := time.Now().Add(5 * time.Second)
	for lg.Get(hookTestKey) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	l := lg.Get(hookTestKey)
	if l == nil || !l.WaitForReady(5*time.Second) {
		t.Fatal("listener did not start")
	}
	addr := l.Addr().String()

	// a CA validating with tls-alpn-01 negotiates acme-tls/1 and gets the challenge certificate
	state := hookHandshake(t, addr, hookChallengeSAN, challenge.ProtocolACMETLS1)
	if state.NegotiatedProtocol != challenge.ProtocolACMETLS1 ||
		state.PeerCertificates[0].DNSNames[0] != hookChallengeSAN {
		t.Errorf("tls-alpn-01 handshake = %q %v", state.NegotiatedProtocol, state.PeerCertificates[0].DNSNames)
	}
	// a name the empty store does not cover is issued on demand
	state = hookHandshake(t, addr, hookOnDemandSAN, "h2")
	if state.NegotiatedProtocol != "h2" || state.PeerCertificates[0].DNSNames[0] != hookOnDemandSAN {
		t.Errorf("on-demand handshake = %q %v", state.NegotiatedProtocol, state.PeerCertificates[0].DNSNames)
	}
}
