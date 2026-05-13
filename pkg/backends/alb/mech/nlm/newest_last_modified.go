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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
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
	mech.PoolHolder
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

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return names.MechanismNLM
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

	// Strip resources from the parent context once; reuse for all goroutines
	bareCtx := tctx.ClearResources(r.Context())

	// Capture all responses with per-slot Last-Modified timestamps
	captures := make([]*capture.CaptureResponseWriter, l)
	lastMods := make([]time.Time, l)
	var eg errgroup.Group
	if limit := h.options.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		eg.SetLimit(limit)
	}
	// Fanout to all healthy targets
	for i := range l {
		if hl[i] == nil {
			continue
		}
		eg.Go(func() error {
			// recover so a single bad upstream doesn't crash the proxy; clear
			// the slot so the fallback path doesn't pick a partial capture
			defer mech.RecoverFanoutPanic("nlm", i, func() { captures[i] = nil })
			r2, err := request.CloneWithoutResources(r)
			if err != nil {
				return err
			}
			r2 = r2.WithContext(bareCtx)
			crw := capture.NewCaptureResponseWriter()
			captures[i] = crw
			hl[i].Handler().ServeHTTP(crw, r2)

			if lmStr := crw.Header().Get(headers.NameLastModified); lmStr != "" {
				if lm, err := time.Parse(time.RFC1123, lmStr); err == nil {
					lastMods[i] = lm
				}
			}
			return nil
		})
	}

	// Wait for all responses to complete
	eg.Wait()

	// Find the response with the newest Last-Modified
	newestIdx := -1
	var newestTime time.Time
	for i, lm := range lastMods {
		if !lm.IsZero() && (newestIdx == -1 || lm.After(newestTime)) {
			newestIdx = i
			newestTime = lm
		}
	}

	if newestIdx >= 0 && captures[newestIdx] != nil {
		writeCapture(w, captures[newestIdx])
		return
	}
	// No valid Last-Modified found; prefer a 2xx capture before falling back
	// to the first non-nil response.
	for _, crw := range captures {
		if crw != nil && crw.StatusCode() >= 200 && crw.StatusCode() < 300 {
			writeCapture(w, crw)
			return
		}
	}
	for _, crw := range captures {
		if crw != nil {
			writeCapture(w, crw)
			break
		}
	}
}

func writeCapture(w http.ResponseWriter, crw *capture.CaptureResponseWriter) {
	headers.Merge(w.Header(), crw.Header())
	w.WriteHeader(crw.StatusCode())
	w.Write(crw.Body())
}
