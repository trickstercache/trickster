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

package graphite

import (
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"golang.org/x/sync/errgroup"
)

// fallbackTTL is the object-cache TTL of an unaccelerated render response
const fallbackTTL = time.Minute

// RenderHandler serves /render. A single-target request goes through the
// Delta Proxy Cache (which resolves the step via ParseTimeRangeQuery and
// falls back to the Object Proxy Cache when the request is not
// accelerable). A multi-target request is split into one DPC run per
// target, and the results are merged in target order and rendered once
// (decision D5, option B). A response whose step contradicts the predicted
// one is never served from the accelerated path: the prediction is
// discarded and the request is re-served by plain proxy (7.4).
func (c *Client) RenderHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	qp, body, isBody := params.GetRequestValues(r)
	if isBody {
		// the body is consumed by every clone below; keep it replayable
		request.SetBody(r, body)
	}
	targets := qp[upTarget]
	if len(targets) == 0 {
		targets = qp["target[]"]
	}
	if len(targets) <= 1 {
		c.renderOne(w, r)
		return
	}
	c.renderSplit(w, r, qp, targets)
}

// renderOne runs one request through the DPC, capturing the response so
// that a misprediction can be retracted and an unaccelerated response can
// be learned from
func (c *Client) renderOne(w http.ResponseWriter, r *http.Request) {
	rsc := request.GetResources(r)
	cw := capture.NewCaptureResponseWriterWithLimit(c.captureLimit())
	engines.DeltaProxyCacheRequest(cw, r, c.Modeler())
	rq := c.renderQueryOf(rsc)
	if rq != nil {
		if target, predicted, observed, ok := rq.Mispredicted(); ok {
			c.retract(rq, target, predicted, observed)
			c.reproxy(w, r, rsc)
			return
		}
		if rq.Fallback == parsing.ReasonUnknownStep && cw.StatusCode() == http.StatusOK {
			c.learnFromResponse(rq, cw)
		}
		if c.unmodelable(rq, cw) {
			// the origin answered, but the delta cache could not model the
			// response -- most often because it exceeded
			// max_object_size_bytes, which the delta path must buffer to
			// model but the object path can stream. Degrade rather than
			// turn a working query into a 500.
			logger.Warn("graphite response could not be modeled; serving unaccelerated",
				logging.Pairs{
					"backendName": c.Name(), "statusCode": cw.StatusCode(),
					"detail": "if this repeats, raise max_object_size_bytes for this backend",
				})
			c.fallback(w, r, rsc)
			return
		}
	}
	headers.Merge(w.Header(), cw.Header())
	w.WriteHeader(cw.StatusCode())
	_, _ = w.Write(cw.Body())
}

// renderSplit serves a multi-target request as one DPC run per target
func (c *Client) renderSplit(w http.ResponseWriter, r *http.Request, qp url.Values, targets []string) {
	rsc := request.GetResources(r)
	// the client's render options come from the whole request; it is parsed
	// as a whole first because a decline that splitting cannot fix (format,
	// parse error, a function that is not allowlisted) means one
	// unaccelerated request for everything
	trq, rlo, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		rq, _ := trq.ParsedQuery.(*RenderQuery)
		if rq == nil || (rq.Fallback != parsing.ReasonUnknownStep &&
			rq.Fallback != parsing.ReasonMultiTargetMismatch) {
			c.fallback(w, r, rsc)
			return
		}
		rlo = &timeseries.RequestOptions{ProviderRequest: rq}
	}

	type member struct {
		rsc *request.Resources
		cw  *capture.CaptureResponseWriter
		ok  bool
		rq  *RenderQuery
	}
	members := make([]member, len(targets))
	eg := errgroup.Group{}
	limit := runtime.GOMAXPROCS(0)
	if rsc != nil && rsc.BackendOptions != nil && rsc.BackendOptions.FetchConcurrencyLimit > 0 {
		limit = rsc.BackendOptions.FetchConcurrencyLimit
	}
	eg.SetLimit(limit)
	for i, target := range targets {
		eg.Go(func() error {
			sub, err := request.Clone(r)
			if err != nil {
				return nil
			}
			v := url.Values{}
			for k, vals := range qp {
				if k == upTarget || k == "target[]" {
					continue
				}
				v[k] = vals
			}
			v[upTarget] = []string{target}
			params.SetRequestValues(sub, v)
			mrsc := request.GetResources(sub)
			if mrsc == nil {
				return nil
			}
			mrsc.IsMergeMember = true
			mrsc.TimeRangeQuery, mrsc.TS, mrsc.Response = nil, nil, nil
			cw := capture.NewCaptureResponseWriterWithLimit(c.captureLimit())
			engines.DeltaProxyCacheRequest(cw, sub, c.Modeler())
			rq := c.renderQueryOf(mrsc)
			m := member{rsc: mrsc, cw: cw, rq: rq}
			if rq != nil {
				if target, predicted, observed, ok := rq.Mispredicted(); ok {
					c.retract(rq, target, predicted, observed)
				} else if rq.Fallback == "" && mrsc.TS != nil && cw.StatusCode() == http.StatusOK {
					m.ok = true
				}
			}
			members[i] = m
			return nil
		})
	}
	_ = eg.Wait()

	merged := &dataset.DataSet{Results: []*dataset.Result{{}}}
	// msgpack reports the target expression a series came from, so record
	// which target produced each merged series
	var paths []string
	for i := range members {
		m := &members[i]
		if !m.ok {
			// any target that could not be accelerated makes the whole
			// request unaccelerated: the origin renders all targets
			// consistently (consolidation spans every series)
			c.fallback(w, r, rsc)
			return
		}
		ds, ok := m.rsc.TS.(*dataset.DataSet)
		if !ok || ds == nil {
			c.fallback(w, r, rsc)
			return
		}
		if merged.TimeRangeQuery == nil {
			merged.TimeRangeQuery = ds.TimeRangeQuery
		}
		if len(ds.Results) > 0 && ds.Results[0] != nil {
			merged.Results[0].SeriesList = append(merged.Results[0].SeriesList, ds.Results[0].SeriesList...)
			for range ds.Results[0].SeriesList {
				paths = append(paths, targets[i])
			}
		}
	}
	h := w.Header()
	for i := range members {
		for k, v := range members[i].cw.Header() {
			if k == headers.NameContentLength || k == headers.NameContentType {
				continue
			}
			if _, exists := h[k]; !exists {
				h[k] = v
			}
		}
	}
	if rq := c.renderQueryOf(rsc); rq != nil {
		ro := rq.RenderOptions()
		ro.PathExpressions = paths
		rlo = &timeseries.RequestOptions{ProviderRequest: ro}
	}
	if err := c.Modeler().WireMarshalWriter(merged, rlo, http.StatusOK, w); err != nil {
		logger.Error("graphite render marshal failed", logging.Pairs{"backendName": c.Name(), "error": err.Error()})
	}
}

// renderQueryOf returns the RenderQuery the DPC's parse attached, if any
func (c *Client) renderQueryOf(rsc *request.Resources) *RenderQuery {
	if rsc == nil {
		return nil
	}
	rsc.Lock()
	trq := rsc.TimeRangeQuery
	rsc.Unlock()
	if trq == nil {
		return nil
	}
	rq, _ := trq.ParsedQuery.(*RenderQuery)
	return rq
}

// retract handles a verified step misprediction: it is counted, logged,
// and the prediction discarded, so the response is never served from or
// cached under the predicted key
func (c *Client) retract(rq *RenderQuery, target string, predicted, observed time.Duration) {
	logger.Warn("graphite step misprediction", logging.Pairs{
		"backendName": c.Name(),
		"target":      target, "predicted": predicted.String(), "observed": observed.String(),
	})
	c.resolver.Mispredict(rq.Leaves(), predicted, observed)
}

// unmodelable reports whether an accelerated request failed inside
// Trickster rather than at the origin: the delta cache reports its own
// failures as a bodyless 500, while an origin error is relayed with the
// origin's status and body
func (c *Client) unmodelable(rq *RenderQuery, cw *capture.CaptureResponseWriter) bool {
	return rq.Fallback == "" && cw.StatusCode() == http.StatusInternalServerError &&
		len(cw.Body()) == 0
}

// reproxy serves the original request unaccelerated after a misprediction
func (c *Client) reproxy(w http.ResponseWriter, r *http.Request, rsc *request.Resources) {
	if c.observer != nil {
		c.observer.Fallback(parsing.ReasonMisprediction)
	}
	c.fallback(w, r, rsc)
}

// fallback serves the original, unmodified request through the Object
// Proxy Cache lane, keyed on every parameter (see decline)
func (c *Client) fallback(w http.ResponseWriter, r *http.Request, rsc *request.Resources) {
	qp, body, isBody := params.GetRequestValues(r)
	if isBody {
		request.SetBody(r, body)
	}
	if rsc != nil {
		rsc.Lock()
		rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{CacheKeyElements: OPCKeyElements(qp)}
		rsc.AlternateCacheTTL = fallbackTTL
		rsc.Unlock()
	}
	engines.ObjectProxyCacheRequest(w, r)
}

// learnFromResponse implements learn-on-first-response (decision D1, mode
// A): a single-leaf target served unaccelerated because its step was
// unknown yields a JSON response whose timestamps state the step at this
// age; record it and let the learner fill in the rest of the ladder
func (c *Client) learnFromResponse(rq *RenderQuery, cw *capture.CaptureResponseWriter) {
	if len(rq.Targets) != 1 || rq.Params.Format != model.FormatJSON || cw.Truncated() ||
		rq.Params.MaxDataPoints > 0 {
		// a consolidated response reports a multiple of the native step
		return
	}
	t := rq.Targets[0]
	if t.Class.FixedStep != 0 || len(t.Class.Leaves) != 1 || resolution.IsWildcard(t.Class.Leaves[0]) {
		return
	}
	if ct := cw.Header().Get(headers.NameContentType); ct != "" && !strings.HasPrefix(ct, headers.ValueApplicationJSON) {
		return
	}
	probe := &timeseries.TimeRangeQuery{}
	ts, err := model.UnmarshalTimeseries(cw.Body(), probe)
	if err != nil || probe.Step <= 0 {
		return
	}
	if ds, ok := ts.(*dataset.DataSet); !ok || len(ds.Results) == 0 || len(ds.Results[0].SeriesList) != 1 {
		return
	}
	c.resolver.Observe(t.Class.Leaves, rq.Age, probe.Step)
}

func (c *Client) captureLimit() int {
	if o := c.Configuration(); o != nil && o.MaxCaptureBytes > 0 {
		return o.MaxCaptureBytes
	}
	return capture.DefaultMaxBytes
}
