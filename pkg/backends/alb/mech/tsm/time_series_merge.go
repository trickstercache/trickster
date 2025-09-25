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
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

const ID types.ID = 4
const ShortName types.Name = "tsm"
const Name types.Name = "time_series_merge"

type handler struct {
	pool            pool.Pool
	mergePaths      []string        // paths handled by the alb client that are enabled for tsmerge
	nonmergeHandler types.Mechanism // when methodology is tsmerge, this handler is for non-mergeable paths
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

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	hl := h.pool.Healthy() // should return a fanout list
	l := len(hl)
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan
	if l == 1 {
		hl[0].ServeHTTP(w, r)
		return
	}

	var isMergeablePath bool
	for _, v := range h.mergePaths {
		if strings.HasPrefix(r.URL.Path, v) {
			isMergeablePath = true
			break
		}
	}

	if !isMergeablePath {
		hl[0].ServeHTTP(w, r)
		return
	}

	mgs := GetResponseGates(w, r, hl)
	if len(mgs) == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	SetStatusHeader(w, mgs)

	rsc := request.GetResources(mgs[0].Request)
	if rsc != nil && rsc.ResponseMergeFunc != nil {
		if f, ok := rsc.ResponseMergeFunc.(func(http.ResponseWriter,
			*http.Request, merge.ResponseGates)); ok {
			f(w, r, mgs)
		}
	}
}

// GetResponseGates makes the handler request to each fanout backend and
// returns a collection of responses
func GetResponseGates(w http.ResponseWriter, r *http.Request,
	hl []http.Handler) merge.ResponseGates {
	var wg sync.WaitGroup
	l := len(hl)
	mgs := make(merge.ResponseGates, l)
	for i := range l {
		wg.Go(func() {
			var j = i
			if hl[j] == nil {
				return
			}
			r2, _ := request.Clone(r)
			rsc := request.GetResources(r2)
			rsc.IsMergeMember = true
			mgs[j] = merge.NewResponseGate(w, r2, rsc)
			hl[j].ServeHTTP(mgs[j], r2)
		})
	}
	wg.Wait()
	return mgs.Compress()
}

// SetStatusHeader inspects the X-Trickster-Result header value crafted for each mergeable response
// and aggregates into a single header value for the primary merged response
func SetStatusHeader(w http.ResponseWriter, mgs merge.ResponseGates) {
	statusHeader := ""
	for _, mg := range mgs {
		if mg == nil {
			continue
		}
		if h := mg.Header(); h != nil {
			headers.StripMergeHeaders(h)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				h.Get(headers.NameTricksterResult))
		}
	}
	if statusHeader != "" {
		h := w.Header()
		h.Set(headers.NameTricksterResult, statusHeader)
	}
}
