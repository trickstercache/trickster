/*
 * Copyright 2026 The Trickster Authors
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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

const (
	spoofedHop  = "203.0.113.9"
	spoofedReal = "192.0.2.77"
)

// forwardedFront fronts origin with DoProxy, or the passthrough handler, recording peerTrusted on each
// request as the listener's client IP middleware would
func forwardedFront(t *testing.T, originURL, forwarded string, peerTrusted, passthrough bool) *httptest.Server {
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
	o.ForwardedHeaders = forwarded
	client, err := NewTestClient("test", o, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.HTTPClient = client.HTTPClient()
	h := NewPassthroughHandler(client)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(tctx.WithResolvedClient(r.Context(), request.ClientIP(r), peerTrusted))
		r = request.SetResources(r, request.NewResources(o, po.New(), nil, nil, client, nil))
		if passthrough {
			h.ServeHTTP(w, r)
			return
		}
		r.URL = urls.BuildUpstreamURL(r, client.BaseUpstreamURL())
		DoProxy(w, r, true)
	}))
	t.Cleanup(front.Close)
	return front
}

func TestForwardedHopsFromTrustedProxies(t *testing.T) {
	var seen http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	engines := []struct {
		name, forwarded, header, prior string
		passthrough                    bool
	}{
		{"DoProxy/x", "x", headers.NameXForwardedFor, spoofedHop + ",", false},
		{"DoProxy/standard", "standard", headers.NameForwarded, "for=" + spoofedHop + ",", false},
		{"passthrough/x", "x", headers.NameXForwardedFor, spoofedHop + ",", true},
		{"passthrough/standard", "standard", headers.NameForwarded, "for=" + spoofedHop + ",", true},
	}
	for _, e := range engines {
		for _, tc := range []struct {
			name        string
			peerTrusted bool
		}{
			{"untrusted peer", false},
			{"trusted proxy", true},
		} {
			t.Run(e.name+"/"+tc.name, func(t *testing.T) {
				front := forwardedFront(t, origin.URL, e.forwarded, tc.peerTrusted, e.passthrough)
				req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
				req.Header.Set(headers.NameXForwardedFor, spoofedHop)
				req.Header.Set(headers.NameForwarded, "for="+spoofedHop)
				req.Header.Set(headers.NameXRealIP, spoofedReal)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				got := seen.Get(e.header)
				if got == "" {
					t.Fatalf("the origin received no %s", e.header)
				}
				// a trusted proxy's hops are appended to; anyone else's are replaced by this hop alone
				if kept := strings.HasPrefix(got, e.prior); kept != tc.peerTrusted {
					t.Errorf("%s = %q; prior hop kept = %t, want %t", e.header, got, kept, tc.peerTrusted)
				}
				if kept := seen.Get(headers.NameXRealIP) == spoofedReal; kept != tc.peerTrusted {
					t.Errorf("X-Real-IP kept = %t, want %t", kept, tc.peerTrusted)
				}
			})
		}
	}
}
