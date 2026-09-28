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

package engines

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	spanAttrStepAlignment = "step_alignment.mode"
	// parameters are keyed under a prefix, so no parameter name can collide with the body's
	keyElementParamPrefix    = "param:"
	keyElementBody           = "body"
	keyElementValueSeparator = "&"
)

func resolveStepAlignment(ctx context.Context, o *bo.Options, trq *timeseries.TimeRangeQuery,
	tr *tracing.Tracer, span trace.Span,
) {
	if unsupported := trq.ResolveStepAlignment(tctx.StepAlignment(ctx), o.StepAlignment); unsupported != 0 {
		metrics.StepAlignmentFallbacks.WithLabelValues(o.Name, unsupported.String(),
			trq.StepAlignment.String()).Inc()
		logger.Debug("step alignment mode is not supported by the query; using its default",
			logging.Pairs{
				keys.BackendName: o.Name, keys.Requested: unsupported.String(),
				keys.Applied: trq.StepAlignment.String(),
			})
	}
	if trq.StepAlignment != 0 {
		tspan.SetAttributes(tr, span, attribute.String(spanAttrStepAlignment, trq.StepAlignment.String()))
	}
}

func serveUnaligned(w http.ResponseWriter, r *http.Request, rsc *request.Resources,
	trq *timeseries.TimeRangeQuery, rlo *timeseries.RequestOptions, modeler *timeseries.Modeler,
	ttl time.Duration,
) {
	// the origin's answer to the client's own request, through the object proxy cache under a key
	// holding the raw range, for off and for a range with no complete bucket
	if trq.OriginalBody != nil {
		request.SetBody(r, trq.OriginalBody)
	}
	qp, body, isBody := params.GetRequestValues(r)
	elements := unalignedKeyElements(qp, body, isBody, rsc.PathConfig)
	rsc.Lock()
	// the parser's template and key elements name the statement without its range
	trq.TemplateURL, trq.CacheKeyElements = nil, elements
	rsc.AlternateCacheTTL, rsc.PerCredentialCache = ttl, true
	rsc.Unlock()
	if rsc.TSTransformer == nil || modeler == nil {
		ObjectProxyCacheRequest(w, r)
		return
	}
	serveTransformedObject(w, r, rsc, trq, rlo, modeler)
}

func unalignedKeyElements(qp url.Values, body []byte, isBody bool, pc *po.Options) map[string]string {
	// every parameter the path neither replaces nor excludes, and the body
	out := make(map[string]string, len(qp)+1)
	for k, vals := range qp {
		if pc != nil && (pc.ReplacesParam(k) || slices.Contains(pc.CacheKeyParamsExcluded, k)) {
			continue
		}
		// escaped values can't hold the separator, so repeated values never read as one value holding it
		escaped := make([]string, len(vals))
		for i, v := range vals {
			escaped[i] = url.QueryEscape(v)
		}
		out[keyElementParamPrefix+k] = strings.Join(escaped, keyElementValueSeparator)
	}
	if isBody {
		out[keyElementBody] = string(body)
	}
	return out
}

func serveTransformedObject(w http.ResponseWriter, r *http.Request, rsc *request.Resources,
	trq *timeseries.TimeRangeQuery, rlo *timeseries.RequestOptions, modeler *timeseries.Modeler,
) {
	// the response is modeled so the backend's transformations apply to it, as to a delta response
	body, resp, _ := FetchViaObjectProxyCache(r)
	if resp == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	rh := resp.Header.Clone()
	var ts timeseries.Timeseries
	if resp.StatusCode == http.StatusOK && len(body) > 0 {
		var err error
		if ts, err = modeler.WireUnmarshalerReader(getDecoderReader(resp), trq); err != nil {
			ts = nil
		}
	}
	if ts == nil {
		// an error, or a body that can't be modeled, is relayed as the origin sent it
		Respond(w, resp.StatusCode, rh, bytes.NewReader(body))
		return
	}
	rsc.TSTransformer(ts)
	rsc.TS = ts
	// the body is marshaled again, so the origin's length and encoding no longer describe it
	rh.Del(headers.NameContentLength)
	rh.Del(headers.NameContentEncoding)
	rh = setResponseFormat(rh, rlo)
	Respond(w, 0, rh, nil)
	if rsc.IsMergeMember {
		rsc.Response = resp
		return
	}
	modeler.WireMarshalWriter(ts, rlo, resp.StatusCode, w)
}
