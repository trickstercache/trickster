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
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fanout"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
)

const (
	ID   types.ID   = 3
	Name types.Name = "newest_last_modified"
)

type handler struct {
	mech.PoolHolder
	options         options.NewestLastModifiedOptions
	maxCaptureBytes int
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: names.MechanismNLM, New: New}
}

func New(o *options.Options, _ rt.Lookup) (types.Mechanism, error) {
	return &handler{
		options:         o.NLMOptions,
		maxCaptureBytes: o.MaxCaptureBytes,
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
	hl := p.LiveTargets()
	l := len(hl)
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	if l == 1 {
		hl[0].Handler().ServeHTTP(w, r)
		return
	}

	r, err := fanout.PrimeBody(r)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}

	results, _ := fanout.All(r.Context(), r, hl, fanout.Config{
		Mechanism:        "nlm",
		ConcurrencyLimit: h.options.ConcurrencyOptions.GetQueryConcurrencyLimit(),
		MaxCaptureBytes:  h.maxCaptureBytes,
		Context:          tctx.ClearResources,
	})

	newestIdx := -1
	var newestTime time.Time
	for i, res := range results {
		if res.Capture == nil {
			continue
		}
		lmStr := res.Capture.Header().Get(headers.NameLastModified)
		if lmStr == "" {
			continue
		}
		lm, perr := http.ParseTime(lmStr)
		if perr != nil {
			continue
		}
		if newestIdx == -1 || lm.After(newestTime) {
			newestIdx = i
			newestTime = lm
		}
	}

	if newestIdx >= 0 {
		writeCapture(w, results[newestIdx].Capture)
		return
	}
	for _, res := range results {
		if res.Capture != nil && res.Capture.StatusCode() >= 200 && res.Capture.StatusCode() < 300 {
			writeCapture(w, res.Capture)
			return
		}
	}
	for _, res := range results {
		if res.Capture != nil {
			writeCapture(w, res.Capture)
			return
		}
	}
}

func writeCapture(w http.ResponseWriter, crw *capture.CaptureResponseWriter) {
	headers.Merge(w.Header(), crw.Header())
	w.WriteHeader(crw.StatusCode())
	w.Write(crw.Body())
}
