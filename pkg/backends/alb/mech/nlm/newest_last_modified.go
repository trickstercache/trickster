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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"golang.org/x/sync/errgroup"
)

const (
	ID   types.ID   = 3
	Name types.Name = "newest_last_modified"
)

type handler struct {
	pool    pool.Pool
	options options.NewestLastModifiedOptions
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: names.MechanismNLM, New: New}
}

func New(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{
		options: o.NLMOptions,
	}, nil
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
}

func (h *handler) Pool() pool.Pool {
	return h.pool
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
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan
	if l == 1 {
		hl[0].ServeHTTP(w, r)
		return
	}

	// Create contexts for cancellation
	ctx := r.Context()

	// Track the newest Last-Modified response
	newestIdx := -1
	var newestTime time.Time
	var mu sync.Mutex

	// Capture all responses
	captures := fr.GetCapturesSlice(l)
	defer fr.PutCapturesSlice(captures)
	var eg errgroup.Group
	if limit := h.options.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		eg.SetLimit(limit)
	}
	// Fanout to all healthy targets
	for i := range l {
		if hl[i] == nil {
			continue
		}
		idx := i
		eg.Go(func() error {
			r2, _ := request.Clone(r)
			r2 = request.ClearResources(r2.WithContext(ctx))
			crw := capture.GetCaptureResponseWriter()
			captures[idx] = crw
			hl[idx].ServeHTTP(crw, r2)

			if lmStr := crw.Header().Get(headers.NameLastModified); lmStr != "" {
				lm, err := time.Parse(time.RFC1123, lmStr)
				if err == nil && !lm.IsZero() {
					mu.Lock()
					if newestIdx == -1 || lm.After(newestTime) {
						newestIdx = idx
						newestTime = lm
					}
					mu.Unlock()
				}
			}
			return nil
		})
	}

	// Wait for all responses to complete
	eg.Wait()

	// Write the response with the newest Last-Modified
	if newestIdx >= 0 && newestIdx < len(captures) && captures[newestIdx] != nil {
		crw := captures[newestIdx]
		headers.Merge(w.Header(), crw.Header())
		statusCode := crw.StatusCode()
		w.WriteHeader(statusCode)
		w.Write(crw.Body())
		return
	}
	// No valid response found, use the first available
	for _, crw := range captures {
		if crw != nil {
			headers.Merge(w.Header(), crw.Header())
			statusCode := crw.StatusCode()
			w.WriteHeader(statusCode)
			w.Write(crw.Body())
			break
		}
	}
}
