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
	return globalRequest(p.Targets(), p.ConfiguredTargets())
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
