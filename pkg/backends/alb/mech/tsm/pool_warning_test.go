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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/stretchr/testify/require"
)

func TestPoolWarningReachesMergedResponses(t *testing.T) {
	logger.SetLogger(testLogger)
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	targets := pool.Targets{pool.NewTarget(stubMergeHandler("alpha", http.StatusOK), st, nil)}
	h := &handler{mergePaths: []string{"/"}}
	serve := func(a pool.Alignment) string {
		p := pool.NewAligned(targets, -1, a)
		defer p.Stop()
		p.RefreshHealthy()
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTestMergeRequest(t))
		return w.Body.String()
	}
	// a lone member is relayed as it answered, unless its pool's warning must be added to the response
	require.Equal(t, "ok", serve(pool.Alignment{}))
	require.Equal(t, "MERGED:series=alpha|warnings=alpha,pool warning", serve(pool.Alignment{Warning: "pool warning"}))
	require.Equal(t, "ok", serve(pool.Alignment{}))
}

func TestJoinWarnings(t *testing.T) {
	require.Equal(t, "b", joinWarnings("", "b"))
	require.Equal(t, "a; b", joinWarnings("a", "b"))
}
