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

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fanout"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"golang.org/x/sync/errgroup"
)

const (
	FRID   types.ID   = 1
	FRName types.Name = "first_response"

	FGRID   types.ID   = 2
	FGRName types.Name = "first_good_response"
)

type handler struct {
	mech.PoolHolder
	fgr             bool
	fgrCodes        sets.Set[int]
	options         options.FirstGoodResponseOptions
	maxCaptureBytes int
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: FRID, Name: FRName, ShortName: names.MechanismFR, New: New}
}

func RegistryEntryFGR() types.RegistryEntry {
	return types.RegistryEntry{ID: FGRID, Name: FGRName, ShortName: names.MechanismFGR, New: NewFGR}
}

func NewFGR(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{
		fgr:             true,
		fgrCodes:        o.FgrCodesLookup,
		options:         o.FGROptions,
		maxCaptureBytes: o.MaxCaptureBytes,
	}, nil
}

func New(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	h := &handler{}
	if o != nil {
		h.maxCaptureBytes = o.MaxCaptureBytes
	}
	return h, nil
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
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := h.Pool()
	if p == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	hl := p.LiveTargets() // should return a fanout list
	l := len(hl)
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan
	if l == 1 {
		hl[0].Handler().ServeHTTP(w, r)
		return
	}
	r, err := fanout.PrimeBody(r)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}
	// otherwise iterate the fanout
	var claimed int64 = -1
	captures := make([]*capture.CaptureResponseWriter, l)
	var eg errgroup.Group
	if limit := h.options.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		eg.SetLimit(limit)
	}
	responseWritten := make(chan struct{}, 1)

	// wmu serializes response writes with the return path to prevent
	// writing to w after ServeHTTP returns (e.g. on context cancellation).
	var wmu sync.Mutex
	var returned bool

	serve := func(crw *capture.CaptureResponseWriter) {
		wmu.Lock()
		defer wmu.Unlock()
		if returned {
			return
		}
		headers.Merge(w.Header(), crw.Header())
		w.WriteHeader(crw.StatusCode())
		w.Write(crw.Body())
		// this signals the response is written
		responseWritten <- struct{}{}
	}

	// fanout to all healthy targets
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	for i := range l {
		if hl[i] == nil {
			continue
		}
		eg.Go(func() error {
			// recover so a single bad upstream doesn't crash the proxy; clear
			// the slot so the fallback path doesn't serve a partial capture
			defer mech.RecoverFanoutPanic("fr", "", i, func() { captures[i] = nil })
			r2, crw, err := fanout.PrepareClone(ctx, r, i, fanout.Config{
				Mechanism:       "fr",
				MaxCaptureBytes: h.maxCaptureBytes,
				Resources:       func(int) *request.Resources { return &request.Resources{Cancelable: true} },
			})
			if err != nil {
				return err
			}
			captures[i] = crw
			hl[i].Handler().ServeHTTP(crw, r2)
			statusCode := crw.StatusCode()
			custom := h.fgr && len(h.fgrCodes) > 0
			isGood := custom && h.fgrCodes.Contains(statusCode)

			if (!h.fgr || (!custom && statusCode < 400) || isGood) && // this checks if the response qualifies as a client response
				atomic.CompareAndSwapInt64(&claimed, -1, int64(i)) { // this checks that the qualifying response is the first response
				serve(crw)
				cancel()
			}
			return nil
		})
	}

	// this is a fallback case for when no qualifying upstream response arrives,
	// the first response is used, regardless of qualification
	go func() {
		eg.Wait()
		// if claimed is still -1, the fallback case must be used
		if !atomic.CompareAndSwapInt64(&claimed, -1, -2) {
			return
		}
		if r.Context().Err() != nil {
			return
		}
		if h.fgr {
			// FGR: no member qualified under fgrCodes; emit 502 rather
			// than serving a disqualified response.
			wmu.Lock()
			defer wmu.Unlock()
			if returned {
				return
			}
			failures.HandleBadGateway(w, r)
			select {
			case responseWritten <- struct{}{}:
			default:
			}
			return
		}
		// FR: serve the first non-nil response regardless of status.
		for _, crw := range captures {
			if crw != nil {
				serve(crw)
				return
			}
		}
		// no member produced any response; emit 502 directly
		wmu.Lock()
		defer wmu.Unlock()
		if returned {
			return
		}
		failures.HandleBadGateway(w, r)
		select {
		case responseWritten <- struct{}{}:
		default:
		}
	}()

	// this prevents ServeHTTP from returning until the response is fully
	// written or the request context is canceled
	select {
	case <-responseWritten:
		return
	case <-r.Context().Done():
		// Acquire wmu to ensure no in-progress serve() write is active.
		// If serve() holds the lock, this blocks until the write completes.
		// If serve() hasn't started, setting returned prevents future writes.
		wmu.Lock()
		returned = true
		wmu.Unlock()
		return
	}
}
