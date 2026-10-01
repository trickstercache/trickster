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

package alb

import (
	"context"
	goerrors "errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
)

const (
	saLeader   = "leader"
	saPeer     = "peer"
	saGraphite = "graphite"
	saRPC      = "rpc"
	saRule     = "rule"
	saInner    = "inner"
	saALB      = "edge"
	saUser     = "alice"
	// a range query on a path the merge mechanism merges, so it fans out to every live member
	saMergePath = "http://alb/api/v1/query_range?query=up&start=0&end=600&step=60"
	// carries a request's name to the members it reaches, so each can record what that request carried
	saRequestID      = "X-Test-Request"
	saWarningsPrefix = "warnings="
	saNoneWarning    = "trickster: pool members [rpc] can't apply step alignment partial_end, so each " +
		"member uses its own and results may be misaligned"
	saTruncateWarning = "trickster: pool members [graphite] can't apply step alignment partial_end, so " +
		"every member uses truncate"
)

// the modes a Prometheus member supports
const saPrometheusModes = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
	timeseries.StepAlignmentDrop | timeseries.StepAlignmentPartialEnd

var saFactories = rt.Lookup{
	providers.Prometheus:        prometheus.NewClient,
	providers.Graphite:          graphite.NewClient,
	providers.ReverseProxyCache: reverseproxycache.NewClient,
}

var saMergeFunc = merge.TimeseriesMergeFunc(nil)

type modeRecorder struct {
	mu      sync.Mutex
	modes   []timeseries.StepAlignment
	byID    map[string]timeseries.StepAlignment
	pause   chan struct{}
	entered chan struct{}
}

func newModeRecorder() *modeRecorder {
	return &modeRecorder{byID: map[string]timeseries.StepAlignment{}, entered: make(chan struct{}, 1)}
}

func (m *modeRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mode := tctx.StepAlignment(r.Context())
	m.mu.Lock()
	m.modes = append(m.modes, mode)
	if id := r.Header.Get(saRequestID); id != "" {
		m.byID[id] = mode
	}
	pause := m.pause
	m.pause = nil
	m.mu.Unlock()
	if pause != nil {
		m.entered <- struct{}{}
		<-pause
	}
	// answers as a merge member, so a merging ALB's response carries its warnings
	if rsc := request.GetResources(r); rsc != nil {
		rsc.TS = &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeries(dataset.SeriesHeader{Name: saLeader}, nil)}}}}
		rsc.MergeFunc, rsc.MergeRespondFunc = saMergeFunc, respondWithWarnings
	}
	w.WriteHeader(http.StatusOK)
}

func respondWithWarnings(w http.ResponseWriter, _ *http.Request, accum *merge.Accumulator, _ int) {
	w.WriteHeader(http.StatusOK)
	if ds, ok := accum.GetTSData().(*dataset.DataSet); ok {
		_, _ = w.Write([]byte(saWarningsPrefix + strings.Join(ds.Warnings, "|")))
	}
}

func (m *modeRecorder) seen() []timeseries.StepAlignment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]timeseries.StepAlignment(nil), m.modes...)
}

func (m *modeRecorder) modeOf(id string) (timeseries.StepAlignment, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mode, ok := m.byID[id]
	return mode, ok
}

func (m *modeRecorder) pauseNext() chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pause = make(chan struct{})
	return m.pause
}

type alignedGraph struct {
	*graph
	recorders map[string]*modeRecorder
}

func newAlignedGraph(t *testing.T) *alignedGraph {
	return &alignedGraph{graph: newGraph(t), recorders: map[string]*modeRecorder{}}
}

func (g *alignedGraph) member(t *testing.T, name, provider string, mode timeseries.StepAlignment) {
	t.Helper()
	o := bo.New()
	o.Name, o.Provider, o.OriginURL, o.StepAlignment = name, provider, "http://127.0.0.1", mode
	rec := newModeRecorder()
	b, err := saFactories[provider](name, o, rec, nil, nil, nil)
	require.NoError(t, err)
	g.clients[name] = b
	g.health[name] = passingStatus()
	g.recorders[name] = rec
}

func (g *alignedGraph) alignedALB(t *testing.T, name string, mode timeseries.StepAlignment,
	o *ao.Options,
) *Client {
	t.Helper()
	opts := bo.New()
	opts.Name, opts.Provider, opts.StepAlignment, opts.ALBOptions = name, providers.ALB, mode, o
	if o.MechanismName == names.MechanismTSM {
		o.OutputFormat = providers.Prometheus
	}
	require.NoError(t, o.Initialize(name))
	var c *Client
	// an ALB in another's pool is reached through its router, as routing registers it
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.entry.ServeHTTP(w, r) })
	cl, err := NewClient(name, opts, router, nil, nil, saFactories)
	require.NoError(t, err)
	c = cl.(*Client)
	g.clients[name] = c
	return c
}

func (g *alignedGraph) discover(t *testing.T, c *Client, name string) {
	t.Helper()
	// replaces the ALB's discovered members with the named one, or with none
	var targets pool.Targets
	if name != "" {
		b := g.clients[name]
		targets = pool.Targets{pool.NewWeightedTarget(b.Router(), passingStatus(), b, 1)}
	}
	require.True(t, c.SetDynamicTargets(targets))
}

func serveThrough(c *Client, ctx context.Context) {
	serveAs(c, ctx, "")
}

func serveAs(c *Client, ctx context.Context, id string) string {
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, saMergePath, nil)
	if id != "" {
		r.Header.Set(saRequestID, id)
	}
	r = request.SetResources(r, request.NewResources(nil, nil, nil, nil, nil, nil))
	w := httptest.NewRecorder()
	c.Handlers()[providers.ALB].ServeHTTP(w, r)
	return w.Body.String()
}

func requireWarning(t *testing.T, body, warning string) {
	t.Helper()
	if warning == "" {
		require.NotContains(t, body, "trickster: pool members")
		return
	}
	require.Contains(t, body, warning)
}

func TestALBAppliesOneStepAlignmentToEveryMember(t *testing.T) {
	tests := []struct {
		name        string
		mechanism   string
		mode, outer timeseries.StepAlignment
		want        timeseries.StepAlignment
	}{
		{"tsm follows its first configured member", names.MechanismTSM, 0, 0, timeseries.StepAlignmentTruncate},
		{"tsm applies its own mode", names.MechanismTSM, timeseries.StepAlignmentOff, 0, timeseries.StepAlignmentOff},
		{
			"an alb pooling this one chose first", names.MechanismTSM, timeseries.StepAlignmentOff,
			timeseries.StepAlignmentPartialEnd, timeseries.StepAlignmentPartialEnd,
		},
		{"rr applies its own mode", names.MechanismRR, timeseries.StepAlignmentPartialEnd, 0, timeseries.StepAlignmentPartialEnd},
		{"rr without a mode leaves members to theirs", names.MechanismRR, 0, 0, 0},
		{"fr applies its own mode", names.MechanismFR, timeseries.StepAlignmentOff, 0, timeseries.StepAlignmentOff},
		{"nlm applies its own mode", names.MechanismNLM, timeseries.StepAlignmentOff, 0, timeseries.StepAlignmentOff},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newAlignedGraph(t)
			g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentTruncate)
			g.member(t, saPeer, providers.Prometheus, timeseries.StepAlignmentOff)
			c := g.alignedALB(t, saALB, test.mode, &ao.Options{
				MechanismName: test.mechanism, Pool: ao.Members(saLeader, saPeer),
			})
			g.start(t)
			ctx := context.Background()
			if test.outer != 0 {
				ctx = tctx.WithStepAlignment(ctx, test.outer)
			}
			serveThrough(c, ctx)
			if test.mechanism == names.MechanismRR {
				// round robin sends one request to each member
				serveThrough(c, ctx)
			}
			for _, name := range []string{saLeader, saPeer} {
				seen := g.recorders[name].seen()
				if test.mechanism == names.MechanismFR && len(seen) == 0 {
					// the first response wins, and the other member may be canceled before it is reached
					continue
				}
				require.Equal(t, []timeseries.StepAlignment{test.want}, seen, name)
			}
			// a pool mechanism applies its pool's mode as it dispatches, so the ALB adds no handler
			require.True(t, c.entry == http.Handler(c.handler))
		})
	}
}

func TestStickyPicksCarryTheirPoolsMode(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, 0)
	g.member(t, saPeer, providers.Prometheus, 0)
	sticky := g.alignedALB(t, saALB, timeseries.StepAlignmentOff, &ao.Options{
		MechanismName: names.MechanismRR, Pool: ao.Members(saLeader), Sticky: &so.Options{Secret: stickySecret},
	})
	// an ALB that follows an outer ALB's session picks its level with its own pool's mode
	g.alignedALB(t, saInner, timeseries.StepAlignmentPartialEnd,
		&ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saPeer)})
	outer := g.alignedALB(t, "outer", 0, &ao.Options{
		MechanismName: names.MechanismRR, Pool: ao.Members(saInner), Sticky: &so.Options{Secret: stickySecret},
	})
	g.start(t)
	serveThrough(sticky, context.Background())
	serveThrough(outer, context.Background())
	require.Equal(t, []timeseries.StepAlignment{timeseries.StepAlignmentOff}, g.recorders[saLeader].seen())
	require.Equal(t, []timeseries.StepAlignment{timeseries.StepAlignmentPartialEnd}, g.recorders[saPeer].seen())
}

func TestUserRouterAppliesItsStepAlignment(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentTruncate)
	c := g.alignedALB(t, saALB, timeseries.StepAlignmentOff, &ao.Options{
		MechanismName: names.MechanismUR,
		UserRouter:    &uropt.Options{DefaultBackend: saLeader, TargetProvider: providers.Prometheus},
	})
	g.start(t)
	serveThrough(c, context.Background())
	require.Equal(t, []timeseries.StepAlignment{timeseries.StepAlignmentOff}, g.recorders[saLeader].seen())
	require.False(t, c.entry == http.Handler(c.handler))
	// a query's directive may choose any mode its targets all support
	require.Equal(t, tctx.StepAlignmentOverride{Mode: timeseries.StepAlignmentOff, Allowed: saPrometheusModes},
		*c.routerOverride.Load())
}

func TestTSMStepAlignmentSurvivesALeaderOutage(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentTruncate)
	g.member(t, saPeer, providers.Prometheus, timeseries.StepAlignmentOff)
	c := g.alignedALB(t, saALB, 0, &ao.Options{
		MechanismName: names.MechanismTSM, Pool: ao.Members(saLeader, saPeer),
	})
	g.start(t)
	g.health[saLeader].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{saPeer}, liveNames(c))
	serveThrough(c, context.Background())
	// the grid comes from the configured leader, so its outage doesn't move every open dashboard
	require.Empty(t, g.recorders[saLeader].seen())
	require.Equal(t, []timeseries.StepAlignment{timeseries.StepAlignmentTruncate}, g.recorders[saPeer].seen())
}

func TestValidateStepAlignmentNamesMembersThatCantApplyTheMode(t *testing.T) {
	tests := []struct {
		name      string
		mechanism string
		mode      timeseries.StepAlignment
		pool      []string
		lacking   string
	}{
		{"a mode a member doesn't support", names.MechanismRR, timeseries.StepAlignmentPartial, []string{saLeader}, saLeader},
		{"a member without step alignment", names.MechanismRR, timeseries.StepAlignmentTruncate, []string{saLeader, saRPC}, saRPC},
		{"a nested alb applies what its members all apply", names.MechanismRR, timeseries.StepAlignmentPartialEnd, []string{saLeader, saInner}, saInner},
		{"members applying every mode but their own", names.MechanismRR, timeseries.StepAlignmentOff, []string{saLeader, saGraphite}, ""},
		{"a rule's routes are known only per request", names.MechanismRR, timeseries.StepAlignmentTruncate, []string{saLeader, saRule}, ""},
		{"tsm follows its leader", names.MechanismTSM, 0, []string{saPeer, saLeader}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newAlignedGraph(t)
			g.member(t, saLeader, providers.Prometheus, 0)
			g.member(t, saPeer, providers.Prometheus, timeseries.StepAlignmentOff)
			g.member(t, saGraphite, providers.Graphite, 0)
			g.member(t, saRPC, providers.ReverseProxyCache, 0)
			rule := bo.New()
			rule.Name, rule.Provider = saRule, providers.Rule
			b, err := backends.New(saRule, rule, nil, http.NotFoundHandler(), nil)
			require.NoError(t, err)
			g.clients[saRule] = b
			g.alignedALB(t, saInner, 0, &ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saGraphite)})
			g.alignedALB(t, saALB, test.mode, &ao.Options{MechanismName: test.mechanism, Pool: ao.Members(test.pool...)})
			err = ValidateClients(g.clients)
			if test.lacking == "" {
				require.NoError(t, err)
				return
			}
			require.True(t, goerrors.Is(err, bo.ErrUnsupportedStepAlignment), err)
			require.ErrorContains(t, err, `alb "`+saALB+`": pool members [`+test.lacking+`] can't apply it`)
		})
	}
}

func TestValidateStepAlignmentChecksUserRouterTargets(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, 0)
	g.member(t, saRPC, providers.ReverseProxyCache, 0)
	c := g.alignedALB(t, saALB, timeseries.StepAlignmentOff, &ao.Options{
		MechanismName: names.MechanismUR,
		UserRouter: &uropt.Options{
			DefaultBackend: saLeader, Users: uropt.UserMappingOptionsByUser{saUser: {ToBackend: saRPC}},
		},
	})
	require.ErrorContains(t, c.validateStepAlignment(g.clients), "pool members ["+saRPC+"]")
}

func TestStepAlignmentWarnings(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentTruncate)
	g.member(t, saPeer, providers.Prometheus, timeseries.StepAlignmentOff)
	g.member(t, saGraphite, providers.Graphite, 0)
	g.member(t, saRPC, providers.ReverseProxyCache, 0)
	g.alignedALB(t, "mixed-rr", 0, &ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saPeer, saLeader, saRPC)})
	g.alignedALB(t, "mixed-tsm", 0, &ao.Options{MechanismName: names.MechanismTSM, Pool: ao.Members(saLeader, saPeer)})
	g.alignedALB(t, "uniform-rr", 0, &ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saLeader, saGraphite)})
	g.alignedALB(t, "users", 0, &ao.Options{
		MechanismName: names.MechanismUR,
		UserRouter: &uropt.Options{
			DefaultBackend: saLeader, Users: uropt.UserMappingOptionsByUser{saUser: {ToBackend: saPeer}},
		},
	})
	require.Equal(t, []string{
		`alb "mixed-rr" pool members apply different step alignment modes (peer=off, leader=truncate, ` +
			`rpc=none), and each answers with its own; set step_alignment on the alb to apply one`,
		`alb "mixed-tsm" pool members apply different step alignment modes (leader=truncate, peer=off); ` +
			`the alb has all of them apply truncate`,
	}, StepAlignmentWarnings(g.clients))
}

func TestDiscoveredMembersFallBackToAModeTheyAllApply(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, 0)
	g.member(t, saGraphite, providers.Graphite, 0)
	g.member(t, saRPC, providers.ReverseProxyCache, 0)
	c := g.alignedALB(t, saALB, 0, &ao.Options{MechanismName: names.MechanismTSM, Pool: ao.Members(saLeader)})
	g.start(t)
	require.Equal(t, pool.Alignment{Mode: timeseries.StepAlignmentPartialEnd, Allowed: saPrometheusModes},
		c.Pool().Alignment())

	// a directive may still choose any mode every member supports
	g.discover(t, c, saGraphite)
	require.Equal(t, pool.Alignment{
		Mode: timeseries.StepAlignmentTruncate, Allowed: timeseries.StepAlignmentTruncate | timeseries.StepAlignmentOff,
		Warning: saTruncateWarning,
	}, c.Pool().Alignment())
	require.Contains(t, serveAs(c, context.Background(), ""), saTruncateWarning)
	require.Equal(t, []timeseries.StepAlignment{timeseries.StepAlignmentTruncate}, g.recorders[saGraphite].seen())

	g.discover(t, c, saRPC)
	require.Equal(t, pool.Alignment{Warning: saNoneWarning}, c.Pool().Alignment())
	g.discover(t, c, "")
	require.Equal(t, pool.Alignment{Mode: timeseries.StepAlignmentPartialEnd, Allowed: saPrometheusModes},
		c.Pool().Alignment())
}

func TestPausedRequestKeepsItsPoolsAlignment(t *testing.T) {
	const partialEnd, truncate = timeseries.StepAlignmentPartialEnd, timeseries.StepAlignmentTruncate
	tests := []struct {
		name                        string
		before, after               string
		beforeMode, afterMode       timeseries.StepAlignment
		beforeWarning, afterWarning string
	}{
		{"to a pool that needs truncate", "", saGraphite, partialEnd, truncate, "", saTruncateWarning},
		{"back from a pool that needed truncate", saGraphite, "", truncate, partialEnd, saTruncateWarning, ""},
		{"to a pool with no common mode", "", saRPC, partialEnd, 0, "", saNoneWarning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := newAlignedGraph(t)
			g.member(t, saLeader, providers.Prometheus, 0)
			g.member(t, saGraphite, providers.Graphite, 0)
			g.member(t, saRPC, providers.ReverseProxyCache, 0)
			c := g.alignedALB(t, saALB, 0, &ao.Options{MechanismName: names.MechanismTSM, Pool: ao.Members(saLeader)})
			g.start(t)
			g.discover(t, c, test.before)
			leader := g.recorders[saLeader]
			release := leader.pauseNext()
			first := make(chan string)
			go func() { first <- serveAs(c, context.Background(), "first") }()
			<-leader.entered

			// the pool changes while the first request is still being answered
			g.discover(t, c, test.after)
			requireWarning(t, serveAs(c, context.Background(), "second"), test.afterWarning)
			requireModes(t, g, "second", test.afterMode, saLeader, test.after)

			close(release)
			requireWarning(t, <-first, test.beforeWarning)
			requireModes(t, g, "first", test.beforeMode, saLeader, test.before)
		})
	}
}

func requireModes(t *testing.T, g *alignedGraph, id string, want timeseries.StepAlignment,
	members ...string,
) {
	t.Helper()
	for _, name := range members {
		if name == "" {
			continue
		}
		mode, ok := g.recorders[name].modeOf(id)
		require.True(t, ok, "%s never saw request %s", name, id)
		require.Equal(t, want, mode, "%s answered request %s", name, id)
	}
}

func TestPausedPickKeepsItsPoolsAlignment(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, 0)
	g.member(t, saGraphite, providers.Graphite, 0)
	c := g.alignedALB(t, saALB, timeseries.StepAlignmentPartialEnd,
		&ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saLeader)})
	g.start(t)
	leader := g.recorders[saLeader]
	release := leader.pauseNext()
	first := make(chan string)
	go func() { first <- serveAs(c, context.Background(), "first") }()
	<-leader.entered

	g.discover(t, c, saGraphite)
	// round robin sends one of the two to each member, and both are picked from the new pool
	for _, id := range []string{"second", "third"} {
		serveAs(c, context.Background(), id)
	}
	var picked int
	for _, id := range []string{"second", "third"} {
		for _, name := range []string{saLeader, saGraphite} {
			if mode, ok := g.recorders[name].modeOf(id); ok {
				picked++
				require.Equal(t, timeseries.StepAlignmentTruncate, mode, "%s answered %s", name, id)
			}
		}
	}
	require.Equal(t, 2, picked)
	close(release)
	<-first
	requireModes(t, g, "first", timeseries.StepAlignmentPartialEnd, saLeader)
}

func TestPoolSwapsNeverSplitARequestsAlignment(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	for _, mechanism := range []string{names.MechanismTSM, names.MechanismRR} {
		t.Run(mechanism, func(t *testing.T) {
			g := newAlignedGraph(t)
			g.member(t, saLeader, providers.Prometheus, 0)
			g.member(t, saGraphite, providers.Graphite, 0)
			var mode timeseries.StepAlignment
			if mechanism == names.MechanismRR {
				mode = timeseries.StepAlignmentPartialEnd
			}
			c := g.alignedALB(t, saALB, mode, &ao.Options{MechanismName: mechanism, Pool: ao.Members(saLeader)})
			g.start(t)
			graphite := g.clients[saGraphite]
			discovered := pool.Targets{pool.NewWeightedTarget(graphite.Router(), passingStatus(), graphite, 1)}
			stop, swapped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(swapped)
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					runtime.Gosched()
					if i%2 == 0 {
						c.SetDynamicTargets(discovered)
						continue
					}
					c.SetDynamicTargets(nil)
				}
			}()
			var withGraphite, withoutGraphite int
			// requests run until both pools have answered some, yielding so the swaps interleave on one CPU
			for i := 0; i < 300 || (withGraphite == 0 || withoutGraphite == 0) && i < 100_000; i++ {
				runtime.Gosched()
				id := strconv.Itoa(i)
				body := serveAs(c, context.Background(), id)
				// only the pool that holds graphite needs truncate, and only its merges carry the warning
				if _, ok := g.recorders[saGraphite].modeOf(id); ok {
					withGraphite++
					requireModes(t, g, id, timeseries.StepAlignmentTruncate, saGraphite)
					if mechanism == names.MechanismTSM {
						requireModes(t, g, id, timeseries.StepAlignmentTruncate, saLeader)
						requireWarning(t, body, saTruncateWarning)
					}
					continue
				}
				withoutGraphite++
				if mechanism == names.MechanismTSM {
					// round robin can reach the leader through either pool, but a merge can't
					requireModes(t, g, id, timeseries.StepAlignmentPartialEnd, saLeader)
					requireWarning(t, body, "")
				}
			}
			close(stop)
			<-swapped
			require.Positive(t, withGraphite)
			require.Positive(t, withoutGraphite)
		})
	}
}

func TestNestedALBMembersResolveByConfiguration(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentOff)
	g.alignedALB(t, saInner, 0, &ao.Options{MechanismName: names.MechanismRR, Pool: ao.Members(saLeader)})
	c := g.alignedALB(t, saALB, 0, &ao.Options{MechanismName: names.MechanismTSM, Pool: ao.Members(saInner)})
	g.start(t)
	// a nested ALB's pool may start after this one's, so its mode comes from its members' configuration
	require.Equal(t, pool.Alignment{Mode: timeseries.StepAlignmentOff, Allowed: saPrometheusModes},
		c.Pool().Alignment())
}

func TestResolveStepAlignment(t *testing.T) {
	const truncate, off = timeseries.StepAlignmentTruncate, timeseries.StepAlignmentOff
	members := []memberAlignment{
		{name: saLeader, effective: truncate, applicable: truncate | off},
		{name: saGraphite, effective: truncate, applicable: truncate},
		{name: saRule, applicable: timeseries.StepAlignmentAll},
	}
	mode, lacking := resolveStepAlignment(0, true, members)
	require.Equal(t, truncate, mode)
	require.Empty(t, lacking)
	mode, lacking = resolveStepAlignment(off, true, members)
	require.Equal(t, off, mode)
	require.Equal(t, []string{saGraphite}, lacking)
	mode, lacking = resolveStepAlignment(0, false, members)
	require.Zero(t, mode)
	require.Empty(t, lacking)
	mode, _ = resolveStepAlignment(0, true, nil)
	require.Zero(t, mode)
	require.Equal(t, truncate, fallbackStepAlignment(members))
	require.Zero(t, fallbackStepAlignment(append(members, memberAlignment{name: saRPC})))
}

func TestMemberNames(t *testing.T) {
	require.Nil(t, memberNames(nil))
	require.Equal(t, []string{saLeader, saPeer, saRPC, saGraphite}, memberNames(&ao.Options{
		Pool: ao.Members(saLeader, saPeer, saLeader),
		UserRouter: &uropt.Options{DefaultBackend: saRPC, Users: uropt.UserMappingOptionsByUser{
			"bob": {ToBackend: saPeer}, saUser: {ToBackend: saGraphite}, "carol": nil,
		}},
	}))
}

func TestAlignmentOf(t *testing.T) {
	g := newAlignedGraph(t)
	g.member(t, saLeader, providers.Prometheus, timeseries.StepAlignmentOff)
	require.Equal(t, memberAlignment{name: saPeer}, alignmentOf(saPeer, g.clients, sets.NewStringSet()))
	// a cycle is reported by pool validation, not here
	require.Equal(t, memberAlignment{name: saLeader, applicable: timeseries.StepAlignmentAll},
		alignmentOf(saLeader, g.clients, sets.New([]string{saLeader})))
	got := alignmentOf(saLeader, g.clients, sets.NewStringSet())
	require.Equal(t, timeseries.StepAlignmentOff, got.effective)
}
