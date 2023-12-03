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
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// newestResponseGate is a ResponseWriter that only writes when the muxer selects it based on the
// newness of the response's LastModified header when compared to other responses in the Mux
type newestResponseGate struct {
	http.ResponseWriter
	i, s  int
	ca    bool
	h, wh http.Header
	nrm   *newestResponseMux
}

// newestResponseMux keeps track the index of the newest LastModified time registered
type newestResponseMux struct {
	i        int
	t        time.Time
	mtx      sync.RWMutex
	wg       sync.WaitGroup
	contexts []context.Context
}

func newNewestResponseMux(sz int) *newestResponseMux {
	contexts := make([]context.Context, sz)
	for i := 0; i < sz; i++ {
		contexts[i] = context.Background()
	}
	nrm := &newestResponseMux{i: -1, contexts: contexts}
	nrm.wg.Add(sz)
	return nrm
}

func (nrm *newestResponseMux) registerLM(i int, t time.Time) bool {
	var ok bool
	if t.IsZero() {
		return false
	}
	nrm.mtx.Lock()
	if nrm.t.IsZero() || t.After(nrm.t) {
		nrm.i = i
		nrm.t = t
		ok = true
	}
	nrm.mtx.Unlock()
	return ok
}

func (nrm *newestResponseMux) getNewest() int {
	return nrm.i
}

func (c *Client) handleNewestResponse(w http.ResponseWriter, r *http.Request) {
	hl := c.pool.Next() // should return a fanout list
	l := len(hl)
	if l == 0 {
		handlers.HandleBadGateway(w, r)
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
	wg.Add(l)
	for i := 0; i < l; i++ {
		// only the one of these i fanouts to respond will be mapped back to the end user
		// based on the methodology
		// and the rest will have their contexts canceled
		go func(j int) {
			if hl[j] == nil {
				return
			}
			nrg := newNewestResponseGate(w, j, nrm)
			r2 := r.Clone(nrm.contexts[j])
			hl[j].ServeHTTP(nrg, r2)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

func newNewestResponseGate(w http.ResponseWriter, i int, nrm *newestResponseMux) *newestResponseGate {
	return &newestResponseGate{ResponseWriter: w, h: http.Header{}, i: i, nrm: nrm}
}

func (nrg *newestResponseGate) Header() http.Header {
	return nrg.h
}

func (nrg *newestResponseGate) WriteHeader(i int) {
	nrg.s = i
	nrg.wh = nrg.h
	nrg.h = nil
	lm, err := time.Parse(time.RFC1123, nrg.wh.Get(headers.NameLastModified))
	if err == nil {
		nrg.ca = !nrg.nrm.registerLM(nrg.i, lm)
	}
	nrg.nrm.wg.Done()
}

func (nrg *newestResponseGate) Write(b []byte) (int, error) {
	l := len(b)
	if nrg.ca { // can abort without waiting, since this gate is already proven not to be newest
		return l, nil
	}
	nrg.nrm.wg.Wait()
	if nrg.nrm.getNewest() == nrg.i {
		if len(nrg.wh) > 0 {
			headers.Merge(nrg.ResponseWriter.Header(), nrg.wh)
			nrg.wh = nil
		}
		nrg.ResponseWriter.WriteHeader(nrg.s)
		nrg.ResponseWriter.Write(b)
	}
	return l, nil
}
