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

package tsm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/require"
)

const (
	e2eMetric       = "x_total"
	e2ePod          = "pod"
	e2eReplica      = "replica"
	e2eTenantHeader = "X-Scope-OrgID"
	e2eTenant       = "tenant-a"
	e2eOriginPath   = "/prometheus"
	// e2eStart is a minute-aligned time, in seconds, that every member's samples begin at
	e2eStart = 1_700_000_040
	e2eSteps = 30
)

// remoteReadMember serves counters rising by one each second over remote read, recording tenants
type remoteReadMember struct {
	series []labels.Labels
	// start is the time, in seconds, the member's counters begin at
	start   int64
	mu      sync.Mutex
	tenants []string
}

func (m *remoteReadMember) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.tenants = append(m.tenants, r.Header.Get(e2eTenantHeader))
	m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	data, err := snappy.Decode(nil, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req prompb.ReadRequest
	if err := req.Unmarshal(data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := req.Queries[0]
	matchers := make([]*labels.Matcher, len(q.Matchers))
	for i, pm := range q.Matchers {
		matchers[i] = labels.MustNewMatcher(labels.MatchType(pm.Type), pm.Name, pm.Value)
	}
	res := &prompb.ReadResponse{Results: []*prompb.QueryResult{{}}}
	for _, lset := range m.series {
		matched := true
		for _, matcher := range matchers {
			matched = matched && matcher.Matches(lset.Get(matcher.Name))
		}
		if !matched {
			continue
		}
		ts := &prompb.TimeSeries{Labels: prompb.FromLabels(lset, nil)}
		for i := range e2eSteps * 4 {
			at := (m.start + int64(i)*15) * 1000
			if at >= q.StartTimestampMs && at <= q.EndTimestampMs {
				ts.Samples = append(ts.Samples, prompb.Sample{Timestamp: at, Value: float64(i * 15)})
			}
		}
		res.Results[0].Timeseries = append(res.Results[0].Timeseries, ts)
	}
	out, _ := res.Marshal()
	w.Header().Set(headers.NameContentType, "application/x-protobuf")
	_, _ = w.Write(snappy.Encode(nil, out))
}

func newRemoteReadMember(t *testing.T, name string, start int64, pods ...string) (*remoteReadMember, *pool.Target) {
	t.Helper()
	member := &remoteReadMember{start: start}
	for _, pod := range pods {
		member.series = append(member.series, labels.FromStrings(labels.MetricName, e2eMetric, e2ePod, pod,
			e2eReplica, name))
	}
	srv := httptest.NewServer(member)
	t.Cleanup(srv.Close)
	o := bo.New()
	o.Name = name
	o.Provider = providers.Prometheus
	o.OriginURL = srv.URL + e2eOriginPath
	o.Prometheus = &prop.Options{Flavor: prop.FlavorMimir}
	client, err := prometheus.NewClient(name, o, nil, nil, nil, nil)
	require.NoError(t, err)
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	return member, pool.NewTarget(http.NotFoundHandler(), status, client)
}

func TestServeGlobalOverRemoteRead(t *testing.T) {
	east, eastTarget := newRemoteReadMember(t, "east", e2eStart, "a", "b")
	west, westTarget := newRemoteReadMember(t, "west", e2eStart, "b", "c")
	p := pool.New(pool.Targets{eastTarget, westTarget}, -1)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()
	h := &handler{mergePaths: []string{"/"}, tsmOptions: options.Options{
		Evaluation: options.EvaluationGlobal, ReplicaLabels: []string{e2eReplica},
	}}
	h.SetPool(p)

	serve := func(path string, values url.Values) *httptest.ResponseRecorder {
		req := newTestMergeRequest(t)
		req.URL.Path = path
		req.URL.RawQuery = values.Encode()
		req.Header.Set(e2eTenantHeader, e2eTenant)
		request.GetResources(req).TimeRangeQuery = nil
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	end := strconv.Itoa(e2eStart + 10*60)
	w := serve("/api/v1/query", url.Values{"query": {"count(count by (pod) (x_total))"}, "time": {end}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[`+end+`,"3"]}]}}`,
		w.Body.String())

	// pod b's two replicas count once, so the summed per-second rate of three pods is 3
	w = serve("/api/v1/query_range", url.Values{
		"query": {"round(sum(rate(x_total[5m])))"},
		"start": {strconv.Itoa(e2eStart + 5*60)}, "end": {strconv.Itoa(e2eStart + 6*60)}, "step": {"60"},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[`+
		strconv.Itoa(e2eStart+5*60)+`,"3"],[`+strconv.Itoa(e2eStart+6*60)+`,"3"]]}]}}`, w.Body.String())

	for _, member := range []*remoteReadMember{east, west} {
		member.mu.Lock()
		require.Equal(t, []string{e2eTenant, e2eTenant}, member.tenants, "one read per member per query")
		member.mu.Unlock()
	}
}

func TestServeGlobalResponseLimit(t *testing.T) {
	_, target := newRemoteReadMember(t, "east", e2eStart, "a", "b")
	p := pool.New(pool.Targets{target}, -1)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()
	h := &handler{mergePaths: []string{"/"}, maxCaptureBytes: 16, tsmOptions: options.Options{
		Evaluation: options.EvaluationGlobal,
	}}
	h.SetPool(p)
	req := newTestMergeRequest(t)
	req.URL.Path = "/api/v1/query"
	req.URL.RawQuery = url.Values{"query": {e2eMetric}, "time": {strconv.Itoa(e2eStart + 60)}}.Encode()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "raw data limit")
}

func TestServeGlobalCachedOverRemoteRead(t *testing.T) {
	start := time.Now().Truncate(time.Minute).Add(-3 * time.Hour).Unix()
	member, target := newRemoteReadMember(t, "east", start, "a", "b")
	p := pool.New(pool.Targets{target}, -1)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()
	conf, err := config.Load([]string{
		"-origin-url", "http://127.0.0.1:1", "-provider", providers.Prometheus,
		"-log-level", "error",
	})
	require.NoError(t, err)
	c := cr.LoadCachesFromConfig(conf)["default"]
	t.Cleanup(func() { _ = c.Close() })
	alb := conf.Backends["default"].Clone()
	alb.Name = "cached-global"
	h := &handler{name: alb.Name, mergePaths: []string{"/"}, tsmOptions: options.Options{
		Evaluation: options.EvaluationGlobal,
	}}
	h.SetPool(p)
	h.SetCache(c, alb)

	values := url.Values{
		"query": {"count(x_total)"}, "start": {strconv.FormatInt(start+5*60, 10)},
		"end": {strconv.FormatInt(start+20*60, 10)}, "step": {"60"},
	}
	for i, want := range []string{"status=kmiss", "status=hit"} {
		req := newTestMergeRequest(t)
		req.URL.Path = "/api/v1/query_range"
		req.URL.RawQuery = values.Encode()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Contains(t, w.Header().Get(headers.NameTricksterResult), want, "request %d", i)
	}
	member.mu.Lock()
	defer member.mu.Unlock()
	require.Len(t, member.tenants, 1, "the hit reads no member")
}
