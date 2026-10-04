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
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

const (
	globalQueryPath   = "/api/v1/query"
	globalSeriesPath  = "/api/v1/series"
	globalAnswer      = "global"
	globalSourceAPI   = "fake"
	globalMemberLive  = "live"
	globalMemberDown  = "down"
	globalMemberError = "broken"
	globalMemberPlain = "plain"
	globalReplica     = "replica"
)

var errGlobalSourceTest = errors.New("no endpoint")

type fakeGlobalSource struct{}

func (fakeGlobalSource) SeriesAPI() string { return globalSourceAPI }

type fakeGlobalEvaluator struct {
	mu       sync.Mutex
	requests []backends.TSMGlobalRequest
}

func (e *fakeGlobalEvaluator) Handles(r *http.Request) bool {
	return r.URL.Path == globalQueryPath
}

func (e *fakeGlobalEvaluator) ServeTSMGlobal(w http.ResponseWriter, _ *http.Request, req backends.TSMGlobalRequest) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	_, _ = w.Write([]byte(globalAnswer))
}

func (e *fakeGlobalEvaluator) Requests() []backends.TSMGlobalRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]backends.TSMGlobalRequest(nil), e.requests...)
}

// fakeGlobalBackend provides a source, and an evaluator unless it has none to give
type fakeGlobalBackend struct {
	backends.Backend
	cfg       *bo.Options
	evaluator *fakeGlobalEvaluator
	builds    *atomic.Int32
	settings  *merge.TSMGlobalSettings
	buildErr  error
	sourceErr error
}

func (b *fakeGlobalBackend) Configuration() *bo.Options { return b.cfg }
func (b *fakeGlobalBackend) Name() string               { return b.cfg.Name }

func (b *fakeGlobalBackend) NewTSMGlobalEvaluator(settings merge.TSMGlobalSettings) (backends.TSMGlobalEvaluator, error) {
	b.builds.Add(1)
	if b.buildErr != nil {
		return nil, b.buildErr
	}
	*b.settings = settings
	return b.evaluator, nil
}

func (b *fakeGlobalBackend) TSMGlobalSource() (backends.TSMGlobalSource, error) {
	if b.sourceErr != nil {
		return nil, b.sourceErr
	}
	return fakeGlobalSource{}, nil
}

type globalTestPool struct {
	handler   *handler
	evaluator *fakeGlobalEvaluator
	builds    *atomic.Int32
	settings  *merge.TSMGlobalSettings
}

// newGlobalTestPool builds a global ALB over a live member, a failing one, one whose source errors, and
// one that serves no raw series; buildErr fails building the evaluator
func newGlobalTestPool(t *testing.T, evaluation string, buildErr error) globalTestPool {
	t.Helper()
	logger.SetLogger(testLogger)
	tp := globalTestPool{evaluator: &fakeGlobalEvaluator{}, builds: &atomic.Int32{}, settings: &merge.TSMGlobalSettings{}}
	member := func(name string, sourceErr error) backends.Backend {
		return &fakeGlobalBackend{
			cfg: &bo.Options{Name: name}, evaluator: tp.evaluator, builds: tp.builds,
			settings: tp.settings, buildErr: buildErr, sourceErr: sourceErr,
		}
	}
	members := []struct {
		backend backends.Backend
		status  int32
	}{
		{member(globalMemberLive, nil), healthcheck.StatusPassing},
		{member(globalMemberDown, nil), healthcheck.StatusFailing},
		{member(globalMemberError, errGlobalSourceTest), healthcheck.StatusPassing},
		{&stripKeysStubBackend{cfg: &bo.Options{Name: globalMemberPlain}}, healthcheck.StatusPassing},
	}
	targets := make(pool.Targets, len(members))
	for i, m := range members {
		status := &healthcheck.Status{}
		status.Set(m.status)
		targets[i] = pool.NewTarget(stubMergeHandler(m.backend.Configuration().Name, http.StatusOK), status, m.backend)
	}
	p := pool.New(targets, 0)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()
	tp.handler = &handler{mergePaths: []string{"/"}, tsmOptions: options.Options{
		Evaluation: evaluation, ReplicaLabels: []string{globalReplica},
	}}
	tp.handler.SetPool(p)
	return tp
}

func serveGlobalTest(t *testing.T, h *handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := newTestMergeRequest(t)
	req.URL.Path = path
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestServeGlobalEvaluation(t *testing.T) {
	t.Run("handled requests read every member", func(t *testing.T) {
		tp := newGlobalTestPool(t, options.EvaluationGlobal, nil)
		for range 2 {
			w := serveGlobalTest(t, tp.handler, globalQueryPath)
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, globalAnswer, w.Body.String())
		}
		require.Equal(t, int32(1), tp.builds.Load(), "the evaluator is built once")
		require.Equal(t, []string{globalReplica}, tp.settings.ReplicaLabels)
		require.Equal(t, options.DeduplicationPenalty, tp.settings.Deduplication)
		requests := tp.evaluator.Requests()
		require.Len(t, requests, 2)
		req := requests[0]
		require.Equal(t, []backends.TSMGlobalMember{{Name: globalMemberLive, Source: fakeGlobalSource{}}}, req.Members)
		require.Len(t, req.Unavailable, 3)
		require.Equal(t, backends.TSMGlobalUnavailable{Name: globalMemberDown}, req.Unavailable[0])
		require.Equal(t, globalMemberError, req.Unavailable[1].Name)
		require.ErrorIs(t, req.Unavailable[1].Reason, errGlobalSourceTest)
		require.Equal(t, globalMemberPlain, req.Unavailable[2].Name)
		require.ErrorIs(t, req.Unavailable[2].Reason, errNoGlobalSource)
	})
	t.Run("other requests are merged", func(t *testing.T) {
		tp := newGlobalTestPool(t, options.EvaluationGlobal, nil)
		w := serveGlobalTest(t, tp.handler, globalSeriesPath)
		require.NotEqual(t, globalAnswer, w.Body.String())
		require.Empty(t, tp.evaluator.Requests())
	})
	t.Run("merge evaluation builds no evaluator", func(t *testing.T) {
		tp := newGlobalTestPool(t, options.EvaluationMerge, nil)
		w := serveGlobalTest(t, tp.handler, globalQueryPath)
		require.NotEqual(t, globalAnswer, w.Body.String())
		require.Zero(t, tp.builds.Load())
	})
	t.Run("an evaluator that can't be built fails the request", func(t *testing.T) {
		tp := newGlobalTestPool(t, options.EvaluationGlobal, errGlobalSourceTest)
		for range 2 {
			require.Equal(t, http.StatusBadGateway, serveGlobalTest(t, tp.handler, globalQueryPath).Code)
		}
		require.Equal(t, int32(2), tp.builds.Load(), "a failed build is retried")
	})
}

func TestGlobalEvaluatorRequiresAProvider(t *testing.T) {
	h := &handler{tsmOptions: options.Options{Evaluation: options.EvaluationGlobal}}
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	target := pool.NewTarget(http.NotFoundHandler(), status, &stripKeysStubBackend{cfg: &bo.Options{}})
	_, err := h.globalEvaluator(pool.Targets{nil, target})
	require.ErrorIs(t, err, errNoGlobalEvaluator)
}

func TestGlobalRequestReadsUnlistedLiveMembers(t *testing.T) {
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	backend := &fakeGlobalBackend{cfg: &bo.Options{Name: globalMemberLive}}
	target := pool.NewTarget(http.NotFoundHandler(), status, backend)
	req := globalRequest(pool.Targets{target}, pool.Targets{nil})
	require.Equal(t, []backends.TSMGlobalMember{{Name: globalMemberLive, Source: fakeGlobalSource{}}}, req.Members)
	require.Empty(t, req.Unavailable)
}
