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

// pcfOrigin counts upstream hits and can hold the body open until released,
// which is how the tests prove followers joined one stream vs fetched anew.
type pcfOrigin struct {
	hits    atomic.Int32
	release chan struct{}
	body    string
	headers map[string]string
	status  int
}

func newPCFOrigin(t *testing.T, hold bool) (*pcfOrigin, *httptest.Server) {
	t.Helper()
	o := &pcfOrigin{body: strings.Repeat("d", 128*1024), status: http.StatusOK}
	if hold {
		o.release = make(chan struct{})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		o.hits.Add(1)
		for k, v := range o.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(o.body)))
		w.WriteHeader(o.status)
		w.Write([]byte(o.body[:64]))
		http.NewResponseController(w).Flush()
		if o.release != nil {
			<-o.release
		}
		w.Write([]byte(o.body[64:]))
	}))
	t.Cleanup(s.Close)
	return o, s
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
	origin, srv := newPCFOrigin(t, true)
	h := configHarness(t, addPCFBackend("pcf", srv.URL))
	h.start(t)

	const clients = 3
	var wg sync.WaitGroup
	bodies := make([]string, clients)
	errs := make([]error, clients)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get("http://" + h.BaseAddr + "/pcf/object")
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			bodies[i] = string(b)
			errs[i] = err
		}()
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	close(origin.release)
	wg.Wait()

	require.EqualValues(t, 1, origin.hits.Load(),
		"concurrent GETs for one object must share a single upstream fetch")
	for i := range clients {
		require.NoError(t, errs[i], "client %d", i)
		require.Equal(t, origin.body, bodies[i], "client %d body", i)
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
			origin, srv := newPCFOrigin(t, false)
			origin.headers = tc.headers
			h := configHarness(t, addPCFBackend("pcf"+tc.name, srv.URL))
			h.start(t)

			for range 2 {
				req, err := http.NewRequest(http.MethodGet,
					"http://"+h.BaseAddr+"/pcf"+tc.name+"/object", nil)
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
	origin, srv := newPCFOrigin(t, false)
	h := configHarness(t, addPCFBackend("pcfpost", srv.URL))
	h.start(t)

	for range 2 {
		resp, err := http.Post("http://"+h.BaseAddr+"/pcfpost/submit",
			"text/plain", strings.NewReader("x"))
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	require.EqualValues(t, 2, origin.hits.Load(), "POST must never collapse")
}

func TestPCFTruncationFansOutAsFailure(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "262144")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		http.NewResponseController(w).Flush()
		<-release
		panic(http.ErrAbortHandler) // sever the stream mid-body
	}))
	t.Cleanup(srv.Close)

	h := configHarness(t, addPCFBackend("pcftrunc", srv.URL))
	h.start(t)

	const clients = 2
	var wg sync.WaitGroup
	sawFailure := make([]bool, clients)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get("http://" + h.BaseAddr + "/pcftrunc/object")
			if err != nil {
				sawFailure[i] = true
				return
			}
			defer resp.Body.Close()
			if _, err := io.ReadAll(resp.Body); err != nil {
				sawFailure[i] = true
			}
		}()
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	require.EqualValues(t, 1, hits.Load(), "clients should have shared the doomed fetch")
	for i := range clients {
		require.True(t, sawFailure[i],
			"client %d: a truncated collapse must fail visibly, not present as complete", i)
	}
}
