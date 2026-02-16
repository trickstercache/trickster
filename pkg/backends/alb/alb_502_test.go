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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestALB502WithMultiplePrometheusBackends ensure that multiple backends are supported by the ALB mechanism, and that a 502 is not returned
// on initial request.
func TestALB502WithMultiplePrometheusBackends(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())

	mockResponse := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up","job":"test"},"value":[1234567890,"1"]}]}}`

	var backend1Client, backend2Client *prometheus.Client
	var rsc1, rsc2 *request.Resources

	// Setup backend 1
	backendClient, err := prometheus.NewClient("prom1", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	ts, _, r, _, err := tu.NewTestInstance("",
		backendClient.DefaultPathConfigs, 200, mockResponse, nil, providers.Prometheus,
		"/api/v1/query?query=up", "error")
	require.NoError(t, err)
	defer ts.Close()

	rsc1 = request.GetResources(r)
	backendClient, err = prometheus.NewClient("prom1", rsc1.BackendOptions, nil, rsc1.CacheClient, nil, nil)
	require.NoError(t, err)
	backend1Client = backendClient.(*prometheus.Client)
	rsc1.BackendClient = backend1Client
	rsc1.BackendOptions.HTTPClient = backendClient.HTTPClient()

	// Setup backend 2
	backendClient, err = prometheus.NewClient("prom2", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	ts2, _, r2, _, err := tu.NewTestInstance("",
		backendClient.DefaultPathConfigs, 200, mockResponse, nil, providers.Prometheus,
		"/api/v1/query?query=up", "error")
	require.NoError(t, err)
	defer ts2.Close()

	rsc2 = request.GetResources(r2)
	backendClient, err = prometheus.NewClient("prom2", rsc2.BackendOptions, nil, rsc2.CacheClient, nil, nil)
	require.NoError(t, err)
	backend2Client = backendClient.(*prometheus.Client)
	rsc2.BackendClient = backend2Client
	rsc2.BackendOptions.HTTPClient = backendClient.HTTPClient()

	handler1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil && rsc.IsMergeMember {
			rsc.BackendOptions = rsc1.BackendOptions
			rsc.BackendClient = backend1Client
			rsc.PathConfig = rsc1.PathConfig
			rsc.CacheConfig = rsc1.CacheConfig
			rsc.CacheClient = rsc1.CacheClient
			rsc.Tracer = rsc1.Tracer
		}
		backend1Client.QueryHandler(w, r)
	})

	handler2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil && rsc.IsMergeMember {
			rsc.BackendOptions = rsc2.BackendOptions
			rsc.BackendClient = backend2Client
			rsc.PathConfig = rsc2.PathConfig
			rsc.CacheConfig = rsc2.CacheConfig
			rsc.CacheClient = rsc2.CacheClient
			rsc.Tracer = rsc2.Tracer
		}
		backend2Client.QueryHandler(w, r)
	})

	t.Run("single_backend_baseline", func(t *testing.T) {
		pool1, _, st1 := albpool.New(-1, []http.Handler{handler1})
		st1[0].Set(1)
		time.Sleep(250 * time.Millisecond)

		albOpts := &ao.Options{
			MechanismName: names.MechanismTSM,
			OutputFormat:  providers.Prometheus,
		}

		tsmMech, err := tsm.New(albOpts, types.Lookup{providers.Prometheus: prometheus.NewClient})
		require.NoError(t, err)
		tsmMech.SetPool(pool1)
		defer tsmMech.StopPool()

		req := httptest.NewRequest("GET", "http://alb/api/v1/query?query=up", nil)
		rsc := request.NewResources(rsc1.BackendOptions, rsc1.PathConfig, rsc1.CacheConfig,
			rsc1.CacheClient, nil, rsc1.Tracer)
		rsc.TSReqestOptions = rsc1.TSReqestOptions
		req = request.SetResources(req, rsc)

		w := httptest.NewRecorder()
		tsmMech.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("multiple_backends_instant_query", func(t *testing.T) {
		pool2, _, st2 := albpool.New(-1, []http.Handler{handler1, handler2})
		st2[0].Set(1)
		st2[1].Set(1)
		time.Sleep(250 * time.Millisecond)

		albOpts := &ao.Options{
			MechanismName: names.MechanismTSM,
			OutputFormat:  providers.Prometheus,
		}

		tsmMech, err := tsm.New(albOpts, types.Lookup{providers.Prometheus: prometheus.NewClient})
		require.NoError(t, err)
		tsmMech.SetPool(pool2)
		defer tsmMech.StopPool()

		req := httptest.NewRequest("GET", "http://alb/api/v1/query?query=up", nil)
		rsc := request.NewResources(rsc1.BackendOptions, rsc1.PathConfig, rsc1.CacheConfig,
			rsc1.CacheClient, nil, rsc1.Tracer)
		rsc.TSReqestOptions = rsc1.TSReqestOptions
		req = request.SetResources(req, rsc)

		w := httptest.NewRecorder()
		tsmMech.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})
}
