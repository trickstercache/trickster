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
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/sigv4"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestNewHTTPClient(t *testing.T) {
	// test invalid backend options
	c, err := NewHTTPClient(nil)
	if c != nil {
		t.Errorf("expected nil client, got %v", c)
	}
	if err != nil {
		t.Error(err)
	}

	_, caFile, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Error(err)
	}

	caFileInvalid1 := caFile + ".invalid"
	const caFileInvalid2 = "../../testdata/test.06.cert.pem"

	// test good backend options, no CA
	o := bo.New()
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	// test good backend options, 1 good CA
	o.TLS.CertificateAuthorityPaths = []string{caFile}
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	// test good backend options, 1 bad CA (file not found)
	o.TLS.CertificateAuthorityPaths = []string{caFileInvalid1}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("expected error for no such file or directory on %s", caFileInvalid1)
	}

	// test good backend options, 1 bad CA (junk content)
	o.TLS.CertificateAuthorityPaths = []string{caFileInvalid2}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("expected error for unable to append to CA Certs from file %s", caFileInvalid2)
	}

	o.TLS.CertificateAuthorityPaths = []string{}

	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if err != nil {
		t.Error(err)
	}
	if closer != nil {
		defer closer()
	}

	o.TLS.ClientCertPath = cf
	o.TLS.ClientKeyPath = kf
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	o.TLS.ClientCertPath = "../../testdata/test.05.cert.pem"
	o.TLS.ClientKeyPath = "../../testdata/test.05.key.pem"
	o.TLS.CertificateAuthorityPaths = []string{}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("failed to find any PEM data in key input for file %s", o.TLS.ClientKeyPath)
	}
}

func newH2OfferingServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	if !contains(srv.TLS.NextProtos, "h2") {
		t.Fatalf("test server did not advertise h2 in ALPN: %v", srv.TLS.NextProtos)
	}
	return srv
}

func contains(s []string, v string) bool {
	return slices.Contains(s, v)
}

func newInsecureClient(t *testing.T, timeout time.Duration) *http.Client {
	t.Helper()
	o := bo.New()
	o.Timeout = timeout
	o.TLS.InsecureSkipVerify = true
	c, err := NewHTTPClient(o)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	return c
}

func TestNewHTTPClient_NegotiatesHTTP2OverTLSALPN(t *testing.T) {
	srv := newH2OfferingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	c := newInsecureClient(t, 5*time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("expected HTTP/2, got %s (server X-Proto=%s)", resp.Proto, resp.Header.Get("X-Proto"))
	}
}

func TestNewHTTPClient_ContextCancelMidStream(t *testing.T) {
	releaseHandler := make(chan struct{})
	defer close(releaseHandler)

	srv := newH2OfferingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-releaseHandler:
		case <-r.Context().Done():
		}
	})
	defer srv.Close()

	c := newInsecureClient(t, 10*time.Second)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("Do: %v", err)
	}

	readDone := make(chan error, 1)
	go func() {
		_, e := io.Copy(io.Discard, resp.Body)
		readDone <- e
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case e := <-readDone:
		if e == nil {
			t.Fatalf("expected read error after cancel, got nil")
		}
		if !errors.Is(e, context.Canceled) && !strings.Contains(e.Error(), "canceled") {
			t.Errorf("expected context.Canceled, got %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("read did not unblock after context cancel")
	}
	_ = resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - before; leaked > 4 {
		t.Errorf("possible goroutine leak after cancel: before=%d after=%d delta=%d",
			before, runtime.NumGoroutine(), leaked)
	}
}

func TestNewHTTPClient_TransportEnablesH2(t *testing.T) {
	o := bo.New()
	c, err := NewHTTPClient(o)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Errorf("ForceAttemptHTTP2 should be true to opt into h2 alongside the custom Dial / TLSClientConfig")
	}
	if len(tr.TLSNextProto) != 0 {
		t.Errorf("TLSNextProto should be empty (not used to block h2); got %d entries", len(tr.TLSNextProto))
	}
}

func TestNewHTTPClient_SigV4WrapsIdleCloser(t *testing.T) {
	o := bo.New()
	// Static creds skip the AWS provider chain so unit-test env without
	// ~/.aws or env vars doesn't fail signer construction.
	o.SigV4 = &sigv4.SigV4Config{
		Region:    "us-east-1",
		AccessKey: "AKIATEST",
		SecretKey: "secrettest",
	}
	c, err := NewHTTPClient(o)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	type idleCloser interface{ CloseIdleConnections() }
	ic, ok := c.Transport.(idleCloser)
	if !ok {
		t.Fatalf("SigV4 client Transport %T does not satisfy idleCloser", c.Transport)
	}
	ic.CloseIdleConnections()
}
