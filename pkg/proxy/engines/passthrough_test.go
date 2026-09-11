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

package engines

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// passthroughFor wires a passthrough handler to origin, behind a server so the
// ResponseWriter supports Hijack the way a real listener does.
func passthroughFor(t *testing.T, originURL string, fwd ...string) *httptest.Server {
	t.Helper()
	u, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	o := bo.New()
	o.Name = "test"
	o.Provider = "rp"
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.PathPrefix = ""
	if len(fwd) > 0 {
		o.ForwardedHeaders = fwd[0]
	}
	client, err := NewTestClient("test", o, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.HTTPClient = client.HTTPClient()
	h := NewPassthroughHandler(client)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.NewResources(o, po.New(), nil, nil, client, nil)
		h.ServeHTTP(w, request.SetResources(r, rsc))
	}))
	t.Cleanup(front.Close)
	return front
}

func TestPassthroughUpgradeTunnel(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headers.NameUpgrade) != "websocket" {
			t.Errorf("origin did not receive the Upgrade header: %v", r.Header)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		brw.Flush()
		// echo one line back through the tunnel
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		brw.WriteString("echo:" + line)
		brw.Flush()
	}))
	defer origin.Close()

	front := passthroughFor(t, origin.URL)

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(front.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(headers.NameUpgrade); got != "websocket" {
		t.Errorf("expected Upgrade: websocket to reach the client, got %q", got)
	}

	if _, err = conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	got, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo:ping\n" {
		t.Errorf("tunnel did not carry bytes both ways: got %q", got)
	}
}

func TestPassthroughTrailers(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Te"); got != "trailers" {
			t.Errorf("expected Te: trailers to reach the origin, got %q", got)
		}
		w.Header().Set(headers.NameTrailer, "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("body"))
		w.Header().Set("Grpc-Status", "0")
	}))
	defer origin.Close()

	front := passthroughFor(t, origin.URL)

	req, err := http.NewRequest(http.MethodGet, front.URL+"/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Te", "trailers")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "body" {
		t.Errorf("expected body, got %q", b)
	}
	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Errorf("expected trailer Grpc-Status: 0, got %q (trailers: %v)", got, resp.Trailer)
	}
}

func TestPassthroughForwardsAndStrips(t *testing.T) {
	var seen http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Connection", "X-Secret")
		w.Header().Set("X-Secret", "leaked")
		w.Header().Set(headers.NameContentType, "text/plain")
		w.Write([]byte("ok"))
	}))
	defer origin.Close()

	front := passthroughFor(t, origin.URL, "both")
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
	req.Header.Set("X-Hop", "drop-me")
	req.Header.Set("Connection", "X-Hop")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if seen.Get("X-Hop") != "" {
		t.Error("Connection-listed request header was forwarded upstream")
	}
	if seen.Get(headers.NameXForwardedFor) == "" {
		t.Error("expected X-Forwarded-For from the 'both' forwarding policy")
	}
	if seen.Get(headers.NameForwarded) == "" {
		t.Error("expected Forwarded from the 'both' forwarding policy")
	}
	if resp.Header.Get("X-Secret") != "" {
		t.Error("Connection-listed response header leaked to the client")
	}
	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Error("expected the Trickster result header to be set")
	}
}

func TestPassthroughErrorHandler(t *testing.T) {
	front := passthroughFor(t, "http://127.0.0.1:1")
	resp, err := http.Get(front.URL + "/down")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 for an unreachable origin, got %d", resp.StatusCode)
	}
	if resp.Header.Get(headers.NameTricksterResult) == "" {
		t.Error("expected the Trickster result header on the error response")
	}
}
