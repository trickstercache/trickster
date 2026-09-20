/*
 * Copyright 2026 The Trickster Authors
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
// Package pick is the HTTP adapter for selection strategies: one handler that dispatches each
// request to the pool member a strategy picks, whichever strategy that is.
package pick

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	cfgtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
)

// Options are what the adapter needs beyond the strategy itself.
type Options struct {
	// Key is where a request's affinity key is read from, for a strategy that needs one.
	Key options.KeySource
	// IPv6Prefix is how many leading bits of an IPv6 address form a client_ip key.
	IPv6Prefix int
	// GoodCodes are the response codes that count as a good answer, for a strategy that
	// needs latency; nil counts every response as good.
	GoodCodes *cfgtypes.StatusTable
	// Balancer carries what the balancer itself is configured with, such as passive ejection.
	Balancer lb.BalancerOptions
}

type handler struct {
	mech.PoolHolder
	name     types.Name
	balancer *lb.Balancer
	// resolved once: a strategy pays on dispatch only for what it needs
	tracked   bool
	timed     bool
	key       keyFunc
	goodCodes *cfgtypes.StatusTable
}

// New returns the pool mechanism that serves HTTP with the provided strategy, which the
// mechanism then owns. name is the mechanism's short name.
func New(name types.Name, selector lb.Selector, opts ...Options) types.PickerMechanism {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	b := lb.NewBalancer(selector, o.Balancer)
	h := &handler{
		name: name, balancer: b,
		tracked: b.Needs() != 0, timed: b.Needs().Has(lb.NeedLatency),
	}
	h.goodCodes = o.GoodCodes
	if b.Needs().Has(lb.NeedKey) {
		h.key = newKeyFunc(o.Key, o.IPv6Prefix)
	}
	return h
}

func (h *handler) Name() types.Name {
	return h.name
}

// Picker returns the balancer behind the mechanism, for planes that do not dispatch over HTTP.
func (h *handler) Picker() lb.Picker {
	return h.balancer
}

// SetPool installs the pool that requests are dispatched to; nil leaves the mechanism with none.
func (h *handler) SetPool(p pool.Pool) {
	h.PoolHolder.SetPool(p)
	if p == nil {
		h.balancer.SetPool(nil)
		return
	}
	h.balancer.SetPool(p.Core())
}

func (h *handler) StopPool() {
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

// Balancer returns the mechanism's balancer, whose members carry its runtime stats.
func (h *handler) Balancer() *lb.Balancer {
	return h.balancer
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var flow lb.Flow
	if h.key != nil && r != nil {
		flow = h.key(r)
	}
	pk, ok := h.balancer.Pick(flow)
	if !ok {
		failures.HandleBadGateway(w, r)
		return
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || t.Handler() == nil {
		pk.Done(lb.OutcomeFailed)
		failures.HandleBadGateway(w, r)
		return
	}
	switch {
	case !h.tracked:
		t.Handler().ServeHTTP(w, r)
	case h.timed:
		h.serveTimed(pk, t.Handler(), w, r)
	default:
		h.serveTracked(pk, t.Handler(), w, r)
	}
}

// serveTracked reports the pick as done even when the member's handler panics, so the
// member's in-flight count cannot leak
func (h *handler) serveTracked(pk lb.Pick, member http.Handler, w http.ResponseWriter, r *http.Request) {
	outcome := lb.OutcomeFailed
	defer func() { pk.Done(outcome) }()
	member.ServeHTTP(w, r)
	outcome = lb.OutcomeOK
}

// serveTimed also times the member's first write and judges its answer: a response code
// outside the good set is a failure, and a client that went away is nobody's
func (h *handler) serveTimed(pk lb.Pick, member http.Handler, w http.ResponseWriter, r *http.Request) {
	fw := getWriter(w, pk)
	outcome := lb.OutcomeFailed
	defer func() {
		pk.Done(outcome)
		putWriter(fw)
	}()
	member.ServeHTTP(fw, r)
	switch {
	case r != nil && r.Context().Err() != nil:
		outcome = lb.OutcomeCanceled
	case h.goodCodes == nil || h.goodCodes.Contains(fw.code):
		outcome = lb.OutcomeOK
	}
}
