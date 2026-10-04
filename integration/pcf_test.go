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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/stretchr/testify/require"
)

const (
	pcfObjectPath = "/object"
	pcfSubmitPath = "/submit"
	pcfBodySize   = 128 * 1024
	// more than a response writer buffers, so a held fetch still reaches every client's headers
	pcfHeldBytes   = pcfBodySize / 2
	pcfJoinTimeout = 15 * time.Second
)

// pcfOrigin counts upstream hits and can hold the body open until released,
// which is how the tests prove followers joined one stream vs fetched anew.
type pcfOrigin struct {
	hits     atomic.Int32
	release  chan struct{}
	released sync.Once
	body     string
	headers  map[string]string
	status   int
}

func (o *pcfOrigin) releaseHeld() {
	o.released.Do(func() { close(o.release) })
}

func newPCFOrigin(t *testing.T, hold bool, path string) (*pcfOrigin, *httptest.Server) {
	t.Helper()
	o := &pcfOrigin{body: strings.Repeat("d", pcfBodySize), status: http.StatusOK}
	if hold {
		o.release = make(chan struct{})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// only the test's own requests count: a stray one, such as another daemon's health probe
		// reaching this reused port, is not a fetch
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		o.hits.Add(1)
		for k, v := range o.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(o.body)))
		w.WriteHeader(o.status)
		w.Write([]byte(o.body[:pcfHeldBytes]))
		http.NewResponseController(w).Flush()
		if o.release != nil {
			<-o.release
		}
		w.Write([]byte(o.body[pcfHeldBytes:]))
	}))
	t.Cleanup(s.Close)
	if hold {
		// runs before s.Close, which would otherwise wait forever on a handler a failed test never released
		t.Cleanup(o.releaseHeld)
	}
	return o, s
}

type pcfClient struct {
	done chan struct{}
	body string
	err  error
}

func startPCFClient(t *testing.T, url string) *pcfClient {
	t.Helper()
	// returns once the response's headers arrive, when the request has started or joined a fetch
	c := &pcfClient{done: make(chan struct{})}
	headers := make(chan struct{})
	// uncompressed, since a compressed held prefix is too small to leave the writer's buffer
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	go func() {
		defer close(c.done)
		resp, err := client.Get(url)
		close(headers)
		if err != nil {
			c.err = err
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		c.body, c.err = string(b), err
	}()
	select {
	case <-headers:
	case <-time.After(pcfJoinTimeout):
		t.Fatalf("no response headers from %s", url)
	}
	return c
}

// addPCFBackend is addPassthroughBackend plus progressive collapsed forwarding
// on the default path, which routes it through the Lane A collapse registry.
func addPCFBackend(name, originURL string) func(*tkconfig.Config) {
	return func(c *tkconfig.Config) {
		if c.Backends == nil {
			c.Backends = make(bo.Lookup)
		}
		o := bo.New()
		o.Name = name
		o.Provider = providers.ReverseProxy
		o.OriginURL = originURL
		o.CacheName = "default"
		p := po.New()
		p.Path = "/"
		p.HandlerName = providers.Proxy
		p.MatchTypeName = matching.PathMatchNamePrefix
		p.Methods = methods.AllHTTPMethods()
		p.CollapsedForwardingName = "progressive"
		o.Paths = po.List{p}
		c.Backends[name] = o
	}
}

func TestPCFCollapsesConcurrentGets(t *testing.T) {
	origin, srv := newPCFOrigin(t, true, pcfObjectPath)
	h := configHarness(t, addPCFBackend("pcf", srv.URL))
	h.start(t)
	// cleanups run in reverse, so a failed test releases the fetch before the daemon drains it
	t.Cleanup(origin.releaseHeld)

	// each client in turn has its headers while the origin holds the fetch, so each follower joined it
	clients := make([]*pcfClient, 3)
	for i := range clients {
		clients[i] = startPCFClient(t, "http://"+h.BaseAddr+"/pcf"+pcfObjectPath)
	}
	require.EqualValues(t, 1, origin.hits.Load(),
		"concurrent GETs for one object must share a single upstream fetch")
	origin.releaseHeld()
	for i, c := range clients {
		<-c.done
		require.NoError(t, c.err, "client %d", i)
		require.Equal(t, origin.body, c.body, "client %d body", i)
	}
}

func TestPCFRefusesIneligibleResponses(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		reqMod  func(*http.Request)
	}{
		{"private", map[string]string{"Cache-Control": "private"}, nil},
		{"set-cookie", map[string]string{"Set-Cookie": "session=abc"}, nil},
		{"vary", map[string]string{"Vary": "Accept-Encoding"}, nil},
		{"authorization", nil, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer tok")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, srv := newPCFOrigin(t, false, pcfObjectPath)
			origin.headers = tc.headers
			h := configHarness(t, addPCFBackend("pcf"+tc.name, srv.URL))
			h.start(t)

			for range 2 {
				req, err := http.NewRequest(http.MethodGet,
					"http://"+h.BaseAddr+"/pcf"+tc.name+pcfObjectPath, nil)
				require.NoError(t, err)
				if tc.reqMod != nil {
					tc.reqMod(req)
				}
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				b, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				require.NoError(t, err)
				require.Equal(t, origin.body, string(b),
					"ineligible responses must still be delivered, just not shared")
			}
			require.EqualValues(t, 2, origin.hits.Load(),
				"%s response must not collapse", tc.name)
		})
	}
}

func TestPCFNeverCollapsesPost(t *testing.T) {
	origin, srv := newPCFOrigin(t, false, pcfSubmitPath)
	h := configHarness(t, addPCFBackend("pcfpost", srv.URL))
	h.start(t)

	for range 2 {
		resp, err := http.Post("http://"+h.BaseAddr+"/pcfpost"+pcfSubmitPath,
			"text/plain", strings.NewReader("x"))
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	require.EqualValues(t, 2, origin.hits.Load(), "POST must never collapse")
}

func TestPCFTruncationFansOutAsFailure(t *testing.T) {
	release := make(chan struct{})
	releaseHeld := sync.OnceFunc(func() { close(release) })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pcfObjectPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(2*pcfBodySize))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("p", pcfHeldBytes)))
		http.NewResponseController(w).Flush()
		<-release
		panic(http.ErrAbortHandler) // sever the stream mid-body
	}))
	t.Cleanup(srv.Close)

	h := configHarness(t, addPCFBackend("pcftrunc", srv.URL))
	h.start(t)
	t.Cleanup(releaseHeld)

	clients := make([]*pcfClient, 2)
	for i := range clients {
		clients[i] = startPCFClient(t, "http://"+h.BaseAddr+"/pcftrunc"+pcfObjectPath)
	}
	require.EqualValues(t, 1, hits.Load(), "clients should have shared the doomed fetch")
	releaseHeld()
	for i, c := range clients {
		<-c.done
		require.Error(t, c.err,
			"client %d: a truncated collapse must fail visibly, not present as complete", i)
	}
}
