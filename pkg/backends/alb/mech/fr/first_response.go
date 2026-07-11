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
)

const (
	FRName  types.Name = "first_response"
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
	return types.RegistryEntry{Name: FRName, ShortName: names.MechanismFR, New: New}
}

func RegistryEntryFGR() types.RegistryEntry {
	return types.RegistryEntry{Name: FGRName, ShortName: names.MechanismFGR, New: NewFGR}
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

// qualifies returns the winner predicate for fanout.WaitForFirst. FR (non-
// FGR) takes any captured response; FGR with no custom codes accepts any
// status < 400; FGR with custom codes accepts only configured codes.
// Truncated captures are filtered out by WaitForFirst before predicate is
// called.
func (h *handler) qualifies(r *fanout.Result) bool {
	if !h.fgr {
		return true
	}
	code := r.Capture.StatusCode()
	if len(h.fgrCodes) > 0 {
		return h.fgrCodes.Contains(code)
	}
	return code < 400
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := h.Pool()
	if p == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	hl := p.Targets()
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

	cfg := fanout.Config{
		Mechanism:        "fr",
		ConcurrencyLimit: h.options.ConcurrencyOptions.GetQueryConcurrencyLimit(),
		MaxCaptureBytes:  h.maxCaptureBytes,
		Resources:        func(int) *request.Resources { return &request.Resources{Cancelable: true} },
	}

	winner, results, _ := fanout.WaitForFirst(r.Context(), r, hl, cfg, h.qualifies)
	if r.Context().Err() != nil {
		return
	}
	if winner >= 0 {
		writeCapture(w, results[winner].Capture)
		return
	}
	if h.fgr {
		failures.HandleBadGateway(w, r)
		return
	}
	for _, res := range results {
		if res.Failed || res.Capture == nil {
			continue
		}
		writeCapture(w, res.Capture)
		return
	}
	failures.HandleBadGateway(w, r)
}

func writeCapture(w http.ResponseWriter, crw *capture.CaptureResponseWriter) {
	headers.Merge(w.Header(), crw.Header())
	w.WriteHeader(crw.StatusCode())
	_, _ = w.Write(crw.Body())
}
