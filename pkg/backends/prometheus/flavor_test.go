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

package prometheus

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"

	"github.com/stretchr/testify/require"
)

func flavorClient(t *testing.T, flavor string) *Client {
	t.Helper()
	o := bo.New()
	o.Name = "test"
	o.OriginURL = "https://monitoring.us-east-1.amazonaws.com"
	o.Scheme, o.Host = "https", "monitoring.us-east-1.amazonaws.com"
	if flavor != "" {
		o.Prometheus = &po.Options{Flavor: flavor}
	}
	b, err := NewClient("test", o, nil, nil, nil, nil)
	require.NoError(t, err)
	return b.(*Client)
}

func handlersByPath(c *Client) map[string]string {
	out := map[string]string{}
	for _, p := range c.DefaultPathConfigs(bo.New()) {
		out[p.Path] = p.HandlerName
	}
	return out
}

func TestNoFlavorKeepsTheRouteCatalogue(t *testing.T) {
	c := flavorClient(t, "")
	got := c.DefaultPathConfigs(bo.New())
	want := SupportedPaths(bo.New())
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].Path, got[i].Path)
		require.Equal(t, want[i].HandlerName, got[i].HandlerName)
		require.Equal(t, want[i].Methods, got[i].Methods)
		require.Equal(t, want[i].CacheKeyParams, got[i].CacheKeyParams)
	}
	require.Equal(t, "query=up", c.DefaultHealthCheckConfig().Query)
}

func TestCloudWatchRoutes(t *testing.T) {
	c := flavorClient(t, po.FlavorCloudWatch)
	h := handlersByPath(c)
	for path, want := range map[string]string{
		APIPath + mnQueryRange:  mnQueryRange,
		APIPath + mnQuery:       mnQuery,
		APIPath + mnSeries:      mnSeries,
		APIPath + mnLabels:      "labels",
		APIPath + mnLabel + "/": "labels",
		APIPath + mnTargets:     handlerUnsupported,
		APIPath + mnRules:       handlerUnsupported,
		APIPath + mnMetadata:    handlerUnsupported,
		APIPath + "admin":       handlerUnsupported,
		APIPath:                 handlerUnsupported,
		"/":                     handlerCatchAll,
	} {
		require.Equal(t, want, h[path], path)
	}
	for _, p := range c.DefaultPathConfigs(bo.New()) {
		if p.Path == "/" {
			require.Contains(t, p.Methods, http.MethodPut, "no method may reach the origin")
		}
		if p.Path == APIPath+mnQueryRange || p.Path == APIPath+mnQuery {
			require.Contains(t, p.CacheKeyParams, upLimit)
		}
	}
	hc := c.DefaultHealthCheckConfig()
	require.Equal(t, APIPath+mnQuery, hc.Path)
	require.Equal(t, cloudWatchHealthQuery, hc.Query)
}

func TestAMPRoutes(t *testing.T) {
	h := handlersByPath(flavorClient(t, po.FlavorAMP))
	require.Equal(t, "proxycache", h[APIPath+mnRules])
	require.Equal(t, "proxycache", h[APIPath+mnMetadata])
	require.Equal(t, handlerUnsupported, h[APIPath])
	require.Equal(t, handlerUnsupported, h["/"])
}

func TestUnsupportedHandlerAnswersLocally(t *testing.T) {
	c := flavorClient(t, po.FlavorCloudWatch)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://trickster/", strings.NewReader("Action=PutMetricData"))
	c.HandlerLookup()[handlerUnsupported].ServeHTTP(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "does not support proxying")
}

func TestSeriesCap(t *testing.T) {
	require.Zero(t, flavorClient(t, "").seriesCap("10"))
	c := flavorClient(t, po.FlavorCloudWatch)
	for limit, want := range map[string]int{
		"": 500, "10": 10, "500": 500, "900": 500, "0": 500, "-1": 500, "x": 500,
	} {
		require.Equal(t, want, c.seriesCap(limit), limit)
	}
}

// matrixOrigin answers range queries with series series per query, each covering the requested
// range, flagged as truncated when truncate is set; it counts the requests it serves.
type matrixOrigin struct {
	hits     atomic.Int32
	series   int
	truncate atomic.Bool
}

func (m *matrixOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits.Add(1)
	r.ParseForm()
	start, _ := strconv.ParseFloat(r.Form.Get(upStart), 64)
	end, _ := strconv.ParseFloat(r.Form.Get(upEnd), 64)
	step, _ := strconv.ParseFloat(r.Form.Get(upStep), 64)
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := range m.series {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"metric":{"__name__":"m","i":"%d"},"values":[`, i)
		for ts := start; ts <= end; ts += step {
			if ts > start {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `[%d,"1"]`, int64(ts))
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	if m.truncate.Load() {
		b.WriteString(`,"warnings":["results truncated due to series limit (2)."]`)
	}
	b.WriteString(`}`)
	w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	w.Write([]byte(b.String()))
}

const testTruncatedMarkerSuffix = "|truncated"

type rangeMemoryCache interface {
	cache.Cache
	cache.MemoryCache
}

type observedRangeCache struct {
	rangeMemoryCache
	marker chan struct{}
}

func (c *observedRangeCache) Store(key string, data []byte, ttl time.Duration) error {
	err := c.rangeMemoryCache.Store(key, data, ttl)
	if err == nil && strings.HasSuffix(key, testTruncatedMarkerSuffix) {
		select {
		case c.marker <- struct{}{}:
		default:
		}
	}
	return err
}

func rangeServer(t *testing.T, query string, end time.Time, configure func(*bo.Options),
	wrap ...func(cache.Cache) cache.Cache,
) func(int) *http.Response {
	t.Helper()
	target := func(hours int) string {
		return fmt.Sprintf("/api/v1/query_range?query=%s&start=%d&end=%d&step=60&limit=2",
			url.QueryEscape(query), end.Add(-time.Duration(hours)*time.Hour).Unix(), end.Unix())
	}
	ts, _, r, _, err := tu.NewTestInstance("", nil, 200, "{}", nil, providers.Prometheus, target(1), "error")
	require.NoError(t, err)
	t.Cleanup(ts.Close)

	rsc := request.GetResources(r)
	t.Cleanup(func() { require.NoError(t, rsc.CacheClient.Close()) })
	for _, f := range wrap {
		rsc.CacheClient = f(rsc.CacheClient)
	}
	o := rsc.BackendOptions
	o.FastForwardDisable = true
	configure(o)
	b, err := NewClient("default", o, nil, rsc.CacheClient, nil, nil)
	require.NoError(t, err)
	client := b.(*Client)
	o.HTTPClient = client.HTTPClient()
	for _, p := range client.DefaultPathConfigs(o) {
		if p.Path == APIPath+mnQueryRange {
			rsc.PathConfig = p
		}
	}
	return func(hours int) *http.Response {
		w := httptest.NewRecorder()
		rr := httptest.NewRequest(http.MethodGet, "http://trickster"+target(hours), nil)
		res := request.NewResources(o, rsc.PathConfig, rsc.CacheConfig, rsc.CacheClient, client, rsc.Tracer)
		rr = request.SetResources(rr, res)
		client.QueryRangeHandler(w, rr)
		return w.Result()
	}
}

func cloudWatchRangeServer(t *testing.T, origin *matrixOrigin, query string) (func(int) *http.Response, func()) {
	t.Helper()
	srv := httptest.NewServer(origin)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	end := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	marker := make(chan struct{}, 1)
	serve := rangeServer(t, query, end, func(o *bo.Options) {
		o.Scheme, o.Host = u.Scheme, u.Host
		o.Prometheus = &po.Options{Flavor: po.FlavorCloudWatch}
	}, func(c cache.Cache) cache.Cache {
		mc, ok := c.(rangeMemoryCache)
		require.True(t, ok)
		return &observedRangeCache{rangeMemoryCache: mc, marker: marker}
	})
	awaitMarker := func() {
		t.Helper()
		select {
		case <-marker:
		case <-time.After(5 * time.Second):
			t.Fatal("truncated query marker was not stored")
		}
	}
	return serve, awaitMarker
}

func TestCloudWatchTruncatedRangeIsProxiedNotCached(t *testing.T) {
	origin := &matrixOrigin{series: 2}
	origin.truncate.Store(true)
	serve, awaitMarker := cloudWatchRangeServer(t, origin, `{"truncated.metric"}`)

	resp := serve(6)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "results truncated", "the origin's warning reaches the client")
	require.EqualValues(t, 2, origin.hits.Load(), "the truncated fetch is discarded and the query proxied")

	awaitMarker()
	resp = serve(6)
	resp.Body.Close()
	require.EqualValues(t, 3, origin.hits.Load(), "a marked query is proxied without a fetch")
}

func TestCloudWatchTruncatedDeltaEvictsTheEntry(t *testing.T) {
	origin := &matrixOrigin{series: 2}
	serve, awaitMarker := cloudWatchRangeServer(t, origin, `{"delta.metric"}`)
	resp := serve(6)
	resp.Body.Close()
	require.EqualValues(t, 1, origin.hits.Load())

	origin.truncate.Store(true)
	resp = serve(9) // a partial hit, which fetches the earlier 3 hours
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Contains(t, string(body), "results truncated")
	require.EqualValues(t, 3, origin.hits.Load(), "the truncated delta is discarded and the query proxied")
	awaitMarker()

	origin.truncate.Store(false)
	resp = serve(6)
	resp.Body.Close()
	require.EqualValues(t, 4, origin.hits.Load(), "the marked key is proxied, not served from the evicted entry")
}

func TestCloudWatchCompleteRangeIsCached(t *testing.T) {
	origin := &matrixOrigin{series: 2}
	serve, _ := cloudWatchRangeServer(t, origin, `{"complete.metric"}`)
	for range 2 {
		resp := serve(6)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	require.EqualValues(t, 1, origin.hits.Load(), "a result at the cap without a warning is complete")
}
