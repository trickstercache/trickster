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

package integration

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letsencrypt/pebble/v2/ca"
	"github.com/letsencrypt/pebble/v2/db"
	"github.com/letsencrypt/pebble/v2/va"
	"github.com/letsencrypt/pebble/v2/wfe"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

const (
	acmeTestZone       = "acme.test."
	acmeTestTSIGName   = "trickster-acme."
	acmeTestTSIGSecret = "dHJpY2tzdGVyLWFjbWUtaW50ZWdyYXRpb24tdGVzdC1rZXk="
	acmeTestDNSTTL     = 60
)

type testDNS struct {
	Addr string
	mtx  sync.Mutex
	txt  map[string][]string
}

func startTestDNS(t *testing.T) *testDNS {
	t.Helper()
	// authoritative for acmeTestZone: every name is 127.0.0.1, and TSIG-signed updates set TXT records
	d := &testDNS{txt: make(map[string][]string)}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	d.Addr = tcp.Addr().String()
	udp, err := net.ListenPacket("udp", d.Addr)
	require.NoError(t, err)
	secrets := map[string]string{acmeTestTSIGName: acmeTestTSIGSecret}
	// the default accept function refuses UPDATE messages, which rfc2136 clients send
	accept := func(dns.Header) dns.MsgAcceptAction { return dns.MsgAccept }
	for _, s := range []*dns.Server{
		{Listener: tcp, Handler: d, TsigSecret: secrets, MsgAcceptFunc: accept},
		{PacketConn: udp, Handler: d, TsigSecret: secrets, MsgAcceptFunc: accept},
	} {
		go func() { _ = s.ActivateAndServe() }()
		t.Cleanup(func() { _ = s.Shutdown() })
	}
	return d
}

func (d *testDNS) soa() dns.RR {
	return &dns.SOA{
		Hdr: dns.RR_Header{Name: acmeTestZone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: acmeTestDNSTTL},
		Ns:  "ns." + acmeTestZone, Mbox: "hostmaster." + acmeTestZone,
		Serial: 1, Refresh: 3600, Retry: 600, Expire: 86400, Minttl: acmeTestDNSTTL,
	}
}

func (d *testDNS) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	if r.Opcode == dns.OpcodeUpdate {
		if r.IsTsig() == nil || w.TsigStatus() != nil {
			m.Rcode = dns.RcodeRefused
		} else {
			d.update(r.Ns)
		}
		if t := r.IsTsig(); t != nil {
			m.SetTsig(t.Hdr.Name, t.Algorithm, 300, time.Now().Unix())
		}
		_ = w.WriteMsg(m)
		return
	}
	for _, q := range r.Question {
		name := strings.ToLower(q.Name)
		hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: acmeTestDNSTTL}
		switch q.Qtype {
		case dns.TypeA:
			hdr.Rrtype = dns.TypeA
			m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: net.IPv4(127, 0, 0, 1)})
		case dns.TypeTXT:
			hdr.Rrtype = dns.TypeTXT
			d.mtx.Lock()
			for _, v := range d.txt[name] {
				m.Answer = append(m.Answer, &dns.TXT{Hdr: hdr, Txt: []string{v}})
			}
			d.mtx.Unlock()
		case dns.TypeSOA:
			if name == acmeTestZone {
				m.Answer = append(m.Answer, d.soa())
			}
		}
		if len(m.Answer) == 0 {
			m.Ns = append(m.Ns, d.soa())
		}
	}
	_ = w.WriteMsg(m)
}

func (d *testDNS) update(rrs []dns.RR) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for _, rr := range rrs {
		h := rr.Header()
		name := strings.ToLower(h.Name)
		txt, isTXT := rr.(*dns.TXT)
		switch {
		case h.Class == dns.ClassANY:
			delete(d.txt, name)
		case h.Class == dns.ClassNONE && isTXT:
			d.txt[name] = slicesDelete(d.txt[name], strings.Join(txt.Txt, ""))
		case isTXT:
			d.txt[name] = append(d.txt[name], strings.Join(txt.Txt, ""))
		}
	}
}

func slicesDelete(values []string, v string) []string {
	out := values[:0]
	for _, x := range values {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

type testPebble struct {
	DirectoryURL string
	TrustPath    string
	Roots        *x509.CertPool
	mtx          sync.Mutex
	held         chan struct{}
	holding      bool
}

func startPebble(t *testing.T, httpPort, tlsPort int, dnsAddr string) *testPebble {
	t.Helper()
	// Pebble validates against Trickster's challenge ports, resolving names through dnsAddr
	// no random validation delays or injected bad nonces, so issuance is deterministic and fast
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")
	logger := log.New(io.Discard, "", 0)
	if testing.Verbose() {
		logger = log.New(os.Stderr, "pebble ", log.LstdFlags)
	}
	store := db.NewMemoryStore()
	authority := ca.New(logger, store, "", "ecdsa", 0, 1,
		map[string]ca.Profile{"default": {Description: "default"}})
	validator := va.New(logger, httpPort, tlsPort, false, dnsAddr, store)
	front := wfe.New(logger, store, validator, authority, []string{"pebble.letsencrypt.org"},
		false, false, 0, 0)
	p := &testPebble{held: make(chan struct{})}
	close(p.held)
	handler := front.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mtx.Lock()
		held := p.held
		p.mtx.Unlock()
		select {
		case <-held:
		case <-r.Context().Done():
			return
		}
		handler.ServeHTTP(w, r)
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	p.DirectoryURL = srv.URL + wfe.DirectoryPath
	p.TrustPath = filepath.Join(t.TempDir(), "pebble-wfe.pem")
	require.NoError(t, os.WriteFile(p.TrustPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	p.Roots = x509.NewCertPool()
	require.True(t, p.Roots.AppendCertsFromPEM(authority.GetRootCert(0).PEM()))
	return p
}

func (p *testPebble) Hold(t *testing.T) {
	// ACME requests block until Release, which also runs at cleanup so none hangs
	p.mtx.Lock()
	if !p.holding {
		p.held = make(chan struct{})
		p.holding = true
	}
	p.mtx.Unlock()
	t.Cleanup(p.Release)
}

func (p *testPebble) Release() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.holding {
		close(p.held)
		p.holding = false
	}
}

func (p *testPebble) servedLeaf(addr, serverName string) (*x509.Certificate, error) {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr,
		&tls.Config{ServerName: serverName, RootCAs: p.Roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0], nil
}
