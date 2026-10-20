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
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	reso "github.com/trickstercache/trickster/v2/pkg/proxy/resolution/options"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dnsserver"

	"github.com/stretchr/testify/require"
)

const (
	srvTestOwnerAlpha = "alpha.origins.test."
	srvTestOwnerBravo = "bravo.origins.test."
	srvTestTargetA    = "node-a.test."
	srvTestTargetB    = "node-b.test."
	srvTestCertName   = "example.com."
	srvTestLoopback   = "127.0.0.1"
)

func srvTestOrigin(t *testing.T, body string) (*httptest.Server, uint16) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, uint16(ts.Listener.Addr().(*net.TCPAddr).Port)
}

func srvTestGet(t *testing.T, c *http.Client, url string) (string, error) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b), nil
}

// TestNewHTTPClient_SRVResolution dials two origins that are reachable only on the
// ports their SRV records publish, chosen per request by the URL host
func TestNewHTTPClient_SRVResolution(t *testing.T) {
	_, portA := srvTestOrigin(t, "alpha")
	_, portB := srvTestOrigin(t, "bravo")
	dns := dnsserver.New(t)
	dns.MatchNames()
	dns.Set(dnsclient.TypeSRV,
		dnsserver.SRV(srvTestOwnerAlpha, 30, 10, 1, portA, srvTestTargetA),
		dnsserver.SRV(srvTestOwnerBravo, 30, 10, 1, portB, srvTestTargetB),
	)
	dns.Set(dnsclient.TypeA,
		dnsserver.A(srvTestTargetA, 30, srvTestLoopback),
		dnsserver.A(srvTestTargetB, 30, srvTestLoopback),
	)

	o := bo.New()
	o.Name = "srv-client"
	o.OriginResolution = &reso.Options{Mode: reso.ModeSRV, Resolver: dns.Addr()}
	c, err := NewHTTPClient(o)
	require.NoError(t, err)

	body, err := srvTestGet(t, c, "http://alpha.origins.test/")
	require.NoError(t, err)
	require.Equal(t, "alpha", body)
	body, err = srvTestGet(t, c, "http://bravo.origins.test:8080/")
	require.NoError(t, err)
	require.Equal(t, "bravo", body, "the URL port is ignored in srv mode")

	_, err = srvTestGet(t, c, "http://charlie.origins.test/")
	require.Error(t, err, "a name with no SRV records fails the dial")
}

// TestNewHTTPClient_SRVTargetServerName verifies an https origin against the SRV target
// in target mode, and against the URL host otherwise, while still negotiating HTTP/2
func TestNewHTTPClient_SRVTargetServerName(t *testing.T) {
	ts := newH2OfferingServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	})
	defer ts.Close()
	port := uint16(ts.Listener.Addr().(*net.TCPAddr).Port)
	dns := dnsserver.New(t)
	dns.MatchNames()
	// the httptest certificate names example.com, which the additional section resolves
	dns.Set(dnsclient.TypeSRV, dnsserver.SRV(srvTestOwnerAlpha, 30, 0, 0, port, srvTestCertName))
	dns.SetAdditional(dnsserver.A(srvTestCertName, 30, srvTestLoopback))

	newClient := func(serverName string) *http.Client {
		o := bo.New()
		o.Name = "srv-tls-" + serverName
		o.TLS.CertificateAuthorityPEM = string(pem.EncodeToMemory(&pem.Block{
			Type: "CERTIFICATE", Bytes: ts.Certificate().Raw,
		}))
		o.OriginResolution = &reso.Options{
			Mode: reso.ModeSRV, Resolver: dns.Addr(),
			TLSServerName: serverName,
		}
		c, err := NewHTTPClient(o)
		require.NoError(t, err)
		return c
	}

	proto, err := srvTestGet(t, newClient(reso.ServerNameTarget), "https://alpha.origins.test/")
	require.NoError(t, err)
	require.Equal(t, "HTTP/2.0", proto)

	_, err = srvTestGet(t, newClient(reso.ServerNameOwner), "https://alpha.origins.test/")
	require.Error(t, err, "the certificate does not name the SRV owner")
}

func TestNewOriginDialer(t *testing.T) {
	require.Nil(t, NewOriginDialer(nil))
	require.Nil(t, NewOriginDialer(bo.New()))
}
