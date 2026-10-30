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
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

// retiringEvaluator answers as fakeGlobalEvaluator does, counting its retirements and keeping the last one's context
type retiringEvaluator struct {
	fakeGlobalEvaluator
	retirements atomic.Int32
	ctx         context.Context
}

func (e *retiringEvaluator) Retire(ctx context.Context) {
	e.ctx = ctx
	e.retirements.Add(1)
}

// retiringBackend builds a retiringEvaluator, counting its builds
type retiringBackend struct {
	*fakeGlobalBackend
	ev *retiringEvaluator
}

func (b *retiringBackend) NewTSMGlobalEvaluator(merge.TSMGlobalSettings, backends.TSMGlobalCache,
) (backends.TSMGlobalEvaluator, error) {
	b.builds.Add(1)
	return b.ev, nil
}

func TestHandlerRetireRetiresItsEvaluator(t *testing.T) {
	logger.SetLogger(testLogger)
	builds := &atomic.Int32{}
	ev := &retiringEvaluator{}
	backend := &retiringBackend{
		fakeGlobalBackend: &fakeGlobalBackend{cfg: &bo.Options{Name: globalMemberLive}, builds: builds}, ev: ev,
	}
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	p := pool.New(pool.Targets{pool.NewTarget(stubMergeHandler(globalMemberLive, http.StatusOK), status, backend)}, 0)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()
	h := &handler{mergePaths: testMergePaths("/"), tsmOptions: options.Options{Evaluation: options.EvaluationGlobal}}
	h.SetPool(p)

	w := serveGlobalTest(t, h, globalQueryPath)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, globalAnswer, w.Body.String())
	require.Equal(t, int32(1), builds.Load())

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h.Retire(ctx)
	require.Equal(t, int32(1), ev.retirements.Load())
	require.Same(t, ctx, ev.ctx, "the evaluator waits no longer than the handler's retirement allows")
	// a request reaching the retired handler finds no evaluator, and none is built for it
	require.Equal(t, http.StatusBadGateway, serveGlobalTest(t, h, globalQueryPath).Code)
	require.Equal(t, int32(1), builds.Load())
	_, err := h.globalEvaluator(p.Targets())
	require.ErrorIs(t, err, errGlobalRetired)
	// a second retirement has no evaluator left to retire
	h.Retire(ctx)
	require.Equal(t, int32(1), ev.retirements.Load())
}

func TestHandlerRetireWithoutAnEvaluator(t *testing.T) {
	tp := newGlobalTestPool(t, options.EvaluationGlobal, nil)
	tp.handler.Retire(context.Background())
	require.Equal(t, http.StatusBadGateway, serveGlobalTest(t, tp.handler, globalQueryPath).Code)
	require.Zero(t, tp.builds.Load(), "a retired handler builds no evaluator")
}
