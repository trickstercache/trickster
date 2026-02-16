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

package fr

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const (
	FRID   types.ID   = 1
	FRName types.Name = "first_response"

	FGRID   types.ID   = 2
	FGRName types.Name = "first_good_response"
)

type handler struct {
	pool     pool.Pool
	fgr      bool
	fgrCodes sets.Set[int]
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: FRID, Name: FRName, ShortName: names.MechanismFR, New: New}
}

func RegistryEntryFGR() types.RegistryEntry {
	return types.RegistryEntry{ID: FGRID, Name: FGRName, ShortName: names.MechanismFGR, New: NewFGR}
}

func NewFGR(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{
		fgr:      true,
		fgrCodes: o.FgrCodesLookup,
	}, nil
}

func New(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{}, nil
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
}

func (h *handler) Pool() pool.Pool {
	return h.pool
}

func (h *handler) ID() types.ID {
	if h.fgr {
		return FGRID
	}
	return FRID
}

func (h *handler) Name() types.Name {
	if h.fgr {
		return names.MechanismFGR
	}
	return names.MechanismFR
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
	// otherwise iterate the fanout
	var claimed int64 = -1

	contexts := make([]context.Context, l)
	cancels := make([]context.CancelFunc, l)
	for i := range l {
		contexts[i], cancels[i] = context.WithCancel(r.Context())
	}
	captures := GetCapturesSlice(l)
	responseWritten := getResponseChannel()

	var wg sync.WaitGroup

	serve := func(crw *capture.CaptureResponseWriter) {
		headers.Merge(w.Header(), crw.Header())
		w.WriteHeader(crw.StatusCode())
		w.Write(crw.Body())
		// this signals the response is written
		responseWritten <- struct{}{}
	}

	serveAndCancelOthers := func(i int, crw *capture.CaptureResponseWriter) {
		go func() {
			// cancels all other contexts
			for j, cancel := range cancels {
				if j != i {
					cancel()
				}
			}
		}()
		serve(crw)
	}

	// fanout to all healthy targets
	for i := range l {
		if hl[i] == nil {
			continue
		}
		wg.Go(func() {
			r2, _ := request.Clone(r)
			r2 = r2.WithContext(contexts[i])
			r2 = request.SetResources(r2, &request.Resources{Cancelable: true})
			crw := capture.GetCaptureResponseWriter()
			captures[i] = crw
			hl[i].ServeHTTP(crw, r2)
			statusCode := crw.StatusCode()
			custom := h.fgr && len(h.fgrCodes) > 0
			isGood := custom && h.fgrCodes.Contains(statusCode)
			// this checks if the response qualifies as a client response
			if (!h.fgr || (!custom && statusCode < 400) || isGood) &&
				// this checks that the qualifying response is the first response
				atomic.CompareAndSwapInt64(&claimed, -1, int64(i)) {
				// this serves only the first qualifying response
				serveAndCancelOthers(i, crw)
			}
		})
	}

	// this is a fallback case for when no qualifying upstream response arrives,
	// the first response is used, regardless of qualification
	go func() {
		wg.Wait()
		// if claimed is still -1, the fallback case must be used
		if atomic.CompareAndSwapInt64(&claimed, -1, -2) && r.Context().Err() == nil {
			// this iterates the captures and serves the first non-nil response
			for _, crw := range captures {
				if crw != nil {
					serve(crw)
					break
				}
			}
		}
	}()

	// this prevents ServeHTTP from returning until the response is fully
	// written or the request context is canceled
	select {
	case <-responseWritten:
	case <-r.Context().Done():
	}

	// Wait for all goroutines to complete before cleaning up pooled resources
	wg.Wait()
	PutCapturesSlice(captures)
	putResponseChannel(responseWritten)
}
