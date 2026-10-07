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

// Package handler enforces a rate limiter on an HTTP request.
package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

const planeHTTP = "http"

// RateLimitResponder writes the reject response for one backend. The listener always uses the
// default writer. The interface stays here so the limiter core does not import net/http.
type RateLimitResponder interface {
	WriteRateLimited(http.ResponseWriter, *http.Request, ratelimit.Decision)
}

type attachment struct {
	lim          *ratelimit.Limiter
	name         string
	count        bool
	onFullReject bool
	status       int
	header       http.Header
	body         []byte
	policy       string
	limit        uint32
	window       int64
	extract      func(*http.Request) (uint64, bool)
	decisions    *metrics.RateLimitDecision
	custom       RateLimitResponder
}

// HTTP judges the route limiter around next. A nil next or limiter is next, and does not look up
// a store. client may implement RateLimitResponder; the listener does not pass one.
func HTTP(o *options.Options, client any, next http.Handler) http.Handler {
	if next == nil || o == nil {
		return next
	}
	a := attach(o)
	if responder, ok := client.(RateLimitResponder); ok {
		a.custom = responder
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.serve(w, r, "", next)
	})
}

// Listener judges the listener limiter inside path normalization. ready is the already-normalized
// readiness path; an empty ready exempts nothing. strip removes a client-supplied count marker.
// A nil next returns nil and does not look up a store.
func Listener(o *options.Options, ready string, strip bool, next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	if o == nil && !strip {
		return next
	}
	var a *attachment
	if o != nil {
		a = attach(o)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strip && r != nil {
			r.Header.Del(headers.NameXTricksterRateLimited)
		}
		if a == nil {
			next.ServeHTTP(w, r)
			return
		}
		a.serve(w, r, ready, next)
	})
}

func attach(o *options.Options) *attachment {
	lim := ratelimit.Lookup(o.Name, ratelimit.Shape{
		Keys:       ratelimit.EncodeKeys(o.Keys),
		IPv6Prefix: o.IPv6Prefix,
		Window:     time.Duration(o.Window),
		Limit:      uint32(o.Limit), // #nosec G115 -- Validate bounds limit to uint32
		MaxKeys:    o.MaxKeys,
	}, ratelimit.Policy{Missing: missingOf(o.MissingKey), OnFull: onFullOf(o.MaxKeysAction)})
	return &attachment{
		lim: lim, name: o.Name, count: o.Action == options.ActionCount,
		onFullReject: o.MaxKeysAction == options.OnFullReject,
		status:       o.Status, header: o.Header, body: o.Body, policy: o.PolicyHeaders,
		limit: uint32(o.Limit), window: int64(time.Duration(o.Window)), // #nosec G115 -- Validate bounds limit to uint32
		extract: extractor(o), decisions: metrics.NewRateLimitDecision(o.Name, planeHTTP),
	}
}

func extractor(o *options.Options) func(*http.Request) (uint64, bool) {
	if len(o.KeySources) == 0 {
		return func(*http.Request) (uint64, bool) { return 0, true }
	}
	comp := flowkey.HTTPComposite(o.KeySources, o.IPv6Prefix)
	return func(r *http.Request) (uint64, bool) {
		v := comp(r)
		return v.Hash, v.OK
	}
}

func (a *attachment) serve(w http.ResponseWriter, r *http.Request, ready string, next http.Handler) {
	if ready != "" && r != nil && r.URL != nil && r.URL.Path == ready {
		a.finish(w, r, ratelimit.Decision{
			Result: ratelimit.ResultExempt, Allowed: true, Remaining: a.limit, RetryKnown: true,
			Reset: ratelimit.AgeOut(0, ratelimit.Now(), a.window),
		}, next)
		return
	}
	key, ok := a.extract(r)
	now := ratelimit.Now()
	var d ratelimit.Decision
	if a.count {
		d = a.lim.Count(key, ok, now, 1)
	} else {
		d = a.lim.Take(key, ok, now, 1)
	}
	// Count mode still forwards a full table. The marker is only for a full table that reject would stop.
	if a.count && d.Result == ratelimit.ResultFull && !d.Allowed {
		d.Allowed = true
	}
	a.finish(w, r, d, next)
}

func (a *attachment) finish(w http.ResponseWriter, r *http.Request, d ratelimit.Decision, next http.Handler) {
	if a.decisions != nil {
		a.decisions.Observe(d.Result)
	}
	note(a.name, d)
	if !d.Allowed {
		a.reject(w, r, d)
		return
	}
	if a.count && mark(d, a.onFullReject) {
		r.Header.Add(headers.NameXTricksterRateLimited, a.name)
	}
	if a.policy == options.PolicyNone {
		next.ServeHTTP(w, r)
		return
	}
	next.ServeHTTP(&policyWriter{ResponseWriter: w, a: a, d: d}, r)
}

func mark(d ratelimit.Decision, onFullReject bool) bool {
	if d.Result == ratelimit.ResultCounted {
		return true
	}
	return d.Result == ratelimit.ResultFull && onFullReject
}

func note(name string, d ratelimit.Decision) {
	switch d.Result {
	case ratelimit.ResultLimited, ratelimit.ResultCounted, ratelimit.ResultFull:
	default:
		return
	}
	if logger.DebugEnabled() {
		logger.Debug("rate limit decision", logging.Pairs{keys.Name: name, keys.Result: d.Result.String()})
	}
}

func (a *attachment) reject(w http.ResponseWriter, r *http.Request, d ratelimit.Decision) {
	if a.custom != nil {
		writePolicy(w.Header(), a, d)
		w.Header().Set(headers.NameRetryAfter, strconv.Itoa(ratelimit.RetryAfterSeconds(d.RetryAfter)))
		a.custom.WriteRateLimited(w, r, d)
		return
	}
	dst := w.Header()
	for k, vs := range a.header {
		dst[k] = append([]string(nil), vs...)
	}
	writePolicy(dst, a, d)
	dst.Set(headers.NameRetryAfter, strconv.Itoa(ratelimit.RetryAfterSeconds(d.RetryAfter)))
	status := a.status
	if status == 0 {
		status = http.StatusTooManyRequests
	}
	w.WriteHeader(status)
	if r != nil && r.Method == http.MethodHead || len(a.body) == 0 {
		return
	}
	_, _ = w.Write(a.body)
}

func missingOf(v string) ratelimit.MissingKey {
	if v == options.MissingShared {
		return ratelimit.MissingShared
	}
	return ratelimit.MissingExempt
}

func onFullOf(v string) ratelimit.MaxKeysAction {
	if v == options.OnFullReject {
		return ratelimit.MaxKeysReject
	}
	return ratelimit.MaxKeysAllow
}
