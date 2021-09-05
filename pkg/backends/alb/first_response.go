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
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type firstResponseGate struct {
	http.ResponseWriter
	i        int
	fh       http.Header
	c        *responderClaim
	fgr      bool
	fgrCodes map[int]interface{}
}

func (c *Client) handleFirstResponse(w http.ResponseWriter, r *http.Request) {

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
	wc := newResponderClaim(l)
	var wg sync.WaitGroup
	for i := 0; i < l; i++ {
		wg.Add(1)
		// only the one of these i fanouts to respond will be mapped back to the end user
		// based on the methodology
		// and the rest will have their contexts canceled
		go func(j int) {
			if hl[j] == nil {
				return
			}
			wm := newFirstResponseGate(w, wc, j, c.fgr, c.fgrCodes)
			r2 := r.Clone(wc.contexts[j])
			hl[j].ServeHTTP(wm, r2)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

func newFirstResponseGate(w http.ResponseWriter, c *responderClaim, i int, fgr bool,
	fgrCodes map[int]interface{}) *firstResponseGate {
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
	if (!frg.fgr || !custom && i < 400 || custom && isGood) && frg.c.Claim(frg.i) {
		if len(frg.fh) > 0 {
			headers.Merge(frg.ResponseWriter.Header(), frg.fh)
			frg.fh = nil
		}
		frg.ResponseWriter.WriteHeader(i)
		return
	}
}

func (frg *firstResponseGate) Write(b []byte) (int, error) {
	if frg.c.Claim(frg.i) {
		if len(frg.fh) > 0 {
			headers.Merge(frg.ResponseWriter.Header(), frg.fh)
			frg.fh = nil
		}
		return frg.ResponseWriter.Write(b)
	}
	return len(b), nil
}
