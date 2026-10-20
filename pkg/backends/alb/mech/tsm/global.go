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

package tsm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
)

var (
	errNoGlobalEvaluator = errors.New("no pool member provides tsm global evaluation")
	errNoGlobalSource    = errors.New("member serves no raw series")
	errGlobalRetired     = errors.New("tsm global evaluation is retired")
)

// globalEvaluator returns the ALB's evaluator, built once from the first live member that provides one
func (h *handler) globalEvaluator(live pool.Targets) (backends.TSMGlobalEvaluator, error) {
	if ev := h.evaluator.Load(); ev != nil {
		return *ev, nil
	}
	h.evaluatorMu.Lock()
	defer h.evaluatorMu.Unlock()
	if ev := h.evaluator.Load(); ev != nil {
		return *ev, nil
	}
	if h.retired {
		return nil, errGlobalRetired
	}
	for _, t := range live {
		if t == nil {
			continue
		}
		provider, ok := t.Backend().(backends.TSMGlobalEvaluatorProvider)
		if !ok {
			continue
		}
		settings := h.tsmOptions.GlobalSettings()
		settings.Name = h.name
		settings.MaxResponseBytes = int64(h.maxCaptureBytes)
		if settings.MaxResponseBytes <= 0 {
			settings.MaxResponseBytes = bo.DefaultMaxCaptureBytes
		}
		ev, err := provider.NewTSMGlobalEvaluator(settings, backends.TSMGlobalCache{
			Cache: h.cache, Backend: h.backendOptions, Members: h.globalMembers,
		})
		if err != nil {
			return nil, err
		}
		h.evaluator.Store(&ev)
		return ev, nil
	}
	return nil, errNoGlobalEvaluator
}

// Retire retires the ALB's evaluator once the ALB's requests have drained, waiting no longer than ctx for its work to
// end; the handler builds no evaluator after.
func (h *handler) Retire(ctx context.Context) {
	h.evaluatorMu.Lock()
	h.retired = true
	ev := h.evaluator.Swap(nil)
	h.evaluatorMu.Unlock()
	if ev != nil {
		backends.Retire(ctx, *ev)
	}
}

// SetCache gives the handler the cache and backend options global evaluation keeps its results with,
// replacing an evaluator built without them.
func (h *handler) SetCache(c cache.Cache, o *bo.Options) {
	h.evaluatorMu.Lock()
	defer h.evaluatorMu.Unlock()
	h.cache, h.backendOptions = c, o
	h.evaluator.Store(nil)
}

// globalMembers lists the current pool's members for a cache miss's evaluation
func (h *handler) globalMembers() backends.TSMGlobalRequest {
	p := h.Pool()
	if p == nil {
		return backends.TSMGlobalRequest{}
	}
	return h.globalRequest(p, p.Targets())
}

// globalRequest lists what an evaluation over the live members reads, under the pool's namespace
func (h *handler) globalRequest(p pool.Pool, live pool.Targets) backends.TSMGlobalRequest {
	req := globalRequest(live, p.ConfiguredTargets())
	id := h.globalIdentity(p)
	req.Namespace, req.KeyHeaders = id.namespace, id.keyHeaders
	return req
}

// globalIdentity is a pool's namespace for cached global results and its members' cache key headers;
// it is computed once per pool, so it follows membership, not health
type globalIdentity struct {
	pool       pool.Pool
	namespace  string
	keyHeaders []string
}

// globalIdentity returns p's namespace, computing it the first time p is seen; building a member's source
// reads only its configuration, so no member is contacted
func (h *handler) globalIdentity(p pool.Pool) *globalIdentity {
	if id := h.identity.Load(); id != nil && id.pool == p {
		return id
	}
	d := sha256.New()
	var keyHeaders []string
	for _, t := range p.ConfiguredTargets() {
		if t == nil {
			continue
		}
		d.Write([]byte(t.Name()))
		d.Write([]byte{0})
		if provider, ok := t.Backend().(backends.TSMGlobalSourceProvider); ok {
			if source, err := provider.TSMGlobalSource(); err == nil {
				d.Write([]byte(source.Identity()))
				for _, name := range source.KeyHeaders() {
					if !slices.Contains(keyHeaders, name) {
						keyHeaders = append(keyHeaders, name)
					}
				}
			}
		}
		d.Write([]byte{1})
	}
	slices.Sort(keyHeaders)
	for _, name := range keyHeaders {
		d.Write([]byte(name))
		d.Write([]byte{0})
	}
	id := &globalIdentity{pool: p, namespace: hex.EncodeToString(d.Sum(nil)[:16]), keyHeaders: keyHeaders}
	h.identity.Store(id)
	return id
}

// globalRequest lists the sources of the live members in configured order, then any live member the
// configuration doesn't list, and names each configured member that can't be read
func globalRequest(live, configured pool.Targets) backends.TSMGlobalRequest {
	req := backends.TSMGlobalRequest{Members: make([]backends.TSMGlobalMember, 0, len(live))}
	read := func(t *pool.Target) {
		provider, ok := t.Backend().(backends.TSMGlobalSourceProvider)
		if !ok {
			req.Unavailable = append(req.Unavailable, backends.TSMGlobalUnavailable{
				Name: t.Name(), Reason: errNoGlobalSource,
			})
			return
		}
		source, err := provider.TSMGlobalSource()
		if err != nil {
			req.Unavailable = append(req.Unavailable, backends.TSMGlobalUnavailable{Name: t.Name(), Reason: err})
			return
		}
		req.Members = append(req.Members, backends.TSMGlobalMember{Name: t.Name(), Source: source})
	}
	for _, t := range configured {
		switch {
		case t == nil:
		case slices.Contains(live, t):
			read(t)
		default:
			req.Unavailable = append(req.Unavailable, backends.TSMGlobalUnavailable{Name: t.Name()})
		}
	}
	for _, t := range live {
		if t != nil && !slices.Contains(configured, t) {
			read(t)
		}
	}
	return req
}
