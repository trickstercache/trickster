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
	"strings"
	"sync"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

func (c *Client) handleResponseMerge(w http.ResponseWriter, r *http.Request) {

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

	var isMergeablePath bool
	for _, v := range c.mergePaths {
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
	SetStatusHeader(w, mgs)

	rsc := request.GetResources(mgs[0].Request)
	if rsc != nil && rsc.ResponseMergeFunc != nil {
		if f, ok := rsc.ResponseMergeFunc.(func(http.ResponseWriter,
			*http.Request, merge.ResponseGates)); ok {
			f(w, r, mgs)
		}
	}
}

// GetResponseGates make the client request to each fanout backend and returns a collection of responses
func GetResponseGates(w http.ResponseWriter, r *http.Request, hl []http.Handler) merge.ResponseGates {
	var wg sync.WaitGroup
	var mtx sync.Mutex
	l := len(hl)
	mgs := make(merge.ResponseGates, l)
	wg.Add(l)
	for i := 0; i < l; i++ {
		go func(j int) {
			if hl[j] == nil {
				return
			}
			rsc := &request.Resources{IsMergeMember: true}
			ctx := tctx.WithResources(context.Background(), rsc)
			mtx.Lock()
			r2 := r.Clone(ctx)
			mtx.Unlock()
			mgs[j] = merge.NewResponseGate(w, r2, rsc)
			hl[j].ServeHTTP(mgs[j], r2)
			wg.Done()
		}(i)
	}
	wg.Wait()
	return mgs
}

// SetStatusHeader inspects the X-Trickster-Result header value crafted for each mergeable response
// and aggregates into a single header value for the primary merged response
func SetStatusHeader(w http.ResponseWriter, mgs merge.ResponseGates) {
	statusHeader := ""
	for _, mg := range mgs {
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
