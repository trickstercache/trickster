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

package nlm

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

const (
	ID   types.ID   = 3
	Name types.Name = "newest_last_modified"
)

type handler struct {
	pool pool.Pool
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: names.MechanismNLM, New: New}
}

func New(_ *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{}, nil
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
}

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return names.MechanismNLM
}

func (h *handler) StopPool() {
	if h.pool != nil {
		h.pool.Stop()
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hl := h.pool.Healthy() // should return a fanout list
	l := len(hl)
	if len(hl) == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan
	if l == 1 {
		hl[0].ServeHTTP(w, r)
		return
	}
	// otherwise iterate the fanout
	nrm := newNewestResponseMux(l)
	var wg sync.WaitGroup
	for i := range l {
		// only one of these i fanouts to respond will be mapped back to
		// the end user based on the methodology
		wg.Go(func() {
			if hl[i] == nil {
				return
			}
			nrg := newNewestResponseGate(w, i, nrm)
			r2, _ := request.Clone(r)
			hl[i].ServeHTTP(nrg, r2)
		})
	}
	wg.Wait()
}

// newestResponseGate is a ResponseWriter that only writes when the muxer
// selects it based on the newness of the response's LastModified header when
// compared to other responses in the Mux
type newestResponseGate struct {
	http.ResponseWriter
	i, s  int64
	ca    bool
	h, wh http.Header
	nrm   *newestResponseMux
}

// newestResponseMux is used by the ResponseGate while collecting the fanout
// Responses to track the response slice index representing the Response
// with the newest LastModified header.
type newestResponseMux struct {
	i  int64
	t  atomicx.Time
	wg sync.WaitGroup
}

func newNewestResponseMux(sz int) *newestResponseMux {
	nrm := &newestResponseMux{i: -1}
	nrm.wg.Add(sz)
	return nrm
}

func (nrm *newestResponseMux) registerLM(i int, t time.Time) bool {
	var ok bool
	if t.IsZero() {
		return false
	}
	if nrm.t.Load().IsZero() || t.After(nrm.t.Load()) {
		atomic.StoreInt64(&nrm.i, int64(i))
		nrm.t.Store(t)
		ok = true
	}
	return ok
}

func (nrm *newestResponseMux) getNewest() int64 {
	return atomic.LoadInt64(&nrm.i)
}

func newNewestResponseGate(w http.ResponseWriter, i int,
	nrm *newestResponseMux,
) *newestResponseGate {
	return &newestResponseGate{
		ResponseWriter: w, h: http.Header{},
		i: int64(i), nrm: nrm,
	}
}

func (nrg *newestResponseGate) Header() http.Header {
	return nrg.h
}

func (nrg *newestResponseGate) WriteHeader(i int) {
	nrg.s = int64(i)
	nrg.wh = nrg.h
	nrg.h = nil
	lm, err := time.Parse(time.RFC1123, nrg.wh.Get(headers.NameLastModified))
	if err == nil {
		nrg.ca = !nrg.nrm.registerLM(int(nrg.i), lm)
	}
	nrg.nrm.wg.Done()
}

func (nrg *newestResponseGate) Write(b []byte) (int, error) {
	if nrg.ca { // can abort without waiting, since this gate is already proven
		// not to be newest
		return len(b), nil
	}
	nrg.nrm.wg.Wait()
	if nrg.nrm.getNewest() == nrg.i {
		if len(nrg.wh) > 0 {
			headers.Merge(nrg.ResponseWriter.Header(), nrg.wh)
			nrg.wh = nil
		}
		nrg.ResponseWriter.WriteHeader(int(nrg.s))
		nrg.ResponseWriter.Write(b)
	}
	return len(b), nil
}
