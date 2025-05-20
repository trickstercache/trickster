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
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const ID types.ID = 1
const ShortName types.Name = "fr"
const Name types.Name = "first_response"

const FGRID types.ID = 2
const FGRShortName types.Name = "fgr"
const FGRName types.Name = "first_good_response"

type handler struct {
	pool     pool.Pool
	fgr      bool
	fgrCodes sets.Set[int]
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: ShortName, New: New}
}

func RegistryEntryFGR() types.RegistryEntry {
	return types.RegistryEntry{ID: FGRID, Name: FGRName, ShortName: FGRShortName, New: NewFGR}
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

func (h *handler) ID() types.ID {
	if h.fgr {
		return FGRID
	}
	return ID
}

func (h *handler) Name() types.Name {
	if h.fgr {
		return FGRShortName
	}
	return ShortName
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
	wc := newResponderClaim(l)
	var wg sync.WaitGroup
	wg.Add(l)
	for i := range l {
		// only the one of these i fanouts to respond will be mapped back to the
		// end user based on the methodology and the rest will have their
		// contexts canceled
		go func(j int) {
			if hl[j] == nil {
				wg.Done()
				return
			}
			wm := newFirstResponseGate(w, wc, j, h.fgr)
			r2 := r.Clone(wc.contexts[j])
			hl[j].ServeHTTP(wm, r2)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

type firstResponseGate struct {
	http.ResponseWriter
	i        int
	fh       http.Header
	c        *responderClaim
	fgr      bool
	fgrCodes sets.Set[int]
}

func newFirstResponseGate(w http.ResponseWriter, c *responderClaim, i int,
	fgr bool) *firstResponseGate {
	return &firstResponseGate{ResponseWriter: w, c: c, fh: http.Header{}, i: i, fgr: fgr}
}

func (frg *firstResponseGate) Header() http.Header {
	return frg.fh
}

func (frg *firstResponseGate) WriteHeader(i int) {
	var custom = frg.fgr && len(frg.fgrCodes) > 0
	var isGood bool
	if custom {
		_, isGood = frg.fgrCodes[i]
	}
	if (!frg.fgr || !custom && i < 400 || custom && isGood) && frg.c.Claim(int64(frg.i)) {
		if len(frg.fh) > 0 {
			headers.Merge(frg.ResponseWriter.Header(), frg.fh)
			frg.fh = nil
		}
		frg.ResponseWriter.WriteHeader(i)
		return
	}
}

func (frg *firstResponseGate) Write(b []byte) (int, error) {
	if frg.c.Claim(int64(frg.i)) {
		if len(frg.fh) > 0 {
			headers.Merge(frg.ResponseWriter.Header(), frg.fh)
			frg.fh = nil
		}
		return frg.ResponseWriter.Write(b)
	}
	return len(b), nil
}
