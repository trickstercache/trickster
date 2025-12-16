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
	"strings"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

const (
	ID        types.ID   = 4
	ShortName            = names.MechanismTSM
	Name      types.Name = "time_series_merge"
)

type handler struct {
	pool            pool.Pool
	mergePaths      []string        // paths handled by the alb client that are enabled for tsmerge
	nonmergeHandler types.Mechanism // when methodology is tsmerge, this handler is for non-mergeable paths
	outputFormat    string          // the provider output format (e.g., "prometheus")
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: ShortName, New: New}
}

func New(o *options.Options, factories rt.Lookup) (types.Mechanism, error) {
	nmh, _ := rr.New(nil, nil)
	out := &handler{nonmergeHandler: nmh}
	// this validates the merge configuration for the ALB client as it sets it up
	// First, verify the output format is a support merge provider
	if !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}

	// next, get the factory function required to create a backend handler for the supplied format
	f, ok := factories[o.OutputFormat]
	if !ok {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}
	// now, create a handler for the merge provider based on the supplied factory function
	mc1, err := f(providers.ALB, nil, nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	// convert the new time series handler to a mergeable timeseries handler to get the merge paths
	mc2, ok := mc1.(backends.MergeableTimeseriesBackend)
	if !ok {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}
	// set the merge paths in the ALB client
	out.mergePaths = mc2.MergeablePaths()
	out.outputFormat = o.OutputFormat
	return out, nil
}

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return ShortName
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
	h.nonmergeHandler.SetPool(p)
}

func (h *handler) StopPool() {
	if h.pool != nil {
		h.pool.Stop()
	}
}

func (h *handler) Pool() pool.Pool {
	return h.pool
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hl := h.pool.HealthyTargets() // should return a fanout list
	l := len(hl)
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	defaultHandler := hl[0].Handler()
	var isMergeablePath bool
	for _, v := range h.mergePaths {
		if strings.HasPrefix(r.URL.Path, v) {
			isMergeablePath = true
			break
		}
	}
	if !isMergeablePath {
		defaultHandler.ServeHTTP(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan
	// there are now merge functions attached to the request
	rsc := request.GetResources(r)
	if rsc == nil || l == 1 {
		defaultHandler.ServeHTTP(w, r)
		return
	}

	var mrf merge.RespondFunc

	// Scatter/Gather section

	// Perform streaming merge: each goroutine merges as soon as response arrives
	accumulator := merge.NewAccumulator()
	var wg sync.WaitGroup
	var statusCode int
	var statusHeader string
	var mu sync.Mutex // protects statusCode and statusHeader

	for i := range l {
		if hl[i] == nil {
			continue
		}
		wg.Go(func() {
			r2, _ := request.Clone(r)
			rsc2 := &request.Resources{IsMergeMember: true}
			r2 = request.SetResources(r2, rsc2)
			crw := capture.NewCaptureResponseWriter()
			hl[i].Handler().ServeHTTP(crw, r2)
			rsc2 = request.GetResources(r2)
			if rsc2 == nil {
				logger.Warn("tsm gather failed due to nil resources", nil)
				return
			}

			// Ensure merge functions are set on cloned request
			if rsc2.MergeFunc == nil || rsc2.MergeRespondFunc == nil {
				logger.Warn("tsm gather failed due to nil func", nil)
			}
			// As soon as response is complete, unmarshal and merge
			// This happens in parallel for each response as it arrives
			if rsc2.MergeFunc != nil && rsc2.TS != nil {
				rsc2.MergeFunc(accumulator, rsc2.TS, i)
			}
			// Update status code and headers (take best status code)
			mu.Lock()
			if mrf == nil {
				mrf = rsc2.MergeRespondFunc
			}
			if crw.StatusCode() > 0 {
				if statusCode == 0 || crw.StatusCode() < statusCode {
					statusCode = crw.StatusCode()
				}
			}
			if crw.Header() != nil {
				headers.StripMergeHeaders(crw.Header())
				statusHeader = headers.MergeResultHeaderVals(statusHeader,
					crw.Header().Get(headers.NameTricksterResult))
			}
			mu.Unlock()
		})
	}

	// Wait for all fanout requests to complete
	wg.Wait()

	// Set aggregated status header
	if statusHeader != "" {
		w.Header().Set(headers.NameTricksterResult, statusHeader)
	}

	// Marshal and write the merged series to the client
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if mrf != nil {
		mrf(w, r, accumulator, statusCode)
	}
}
