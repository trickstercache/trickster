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
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	othttptrace "go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Reqs is for Progressive Collapsed Forwarding
var reqs sync.Map

// HTTPBlockSize represents 32K of bytes
const HTTPBlockSize = 32 * 1024

// ClockOffsetWarning is the warning provided to users when the origin's clock offset is suspect
const ClockOffsetWarning = "clock offset between trickster host and origin is high and may cause data anomalies"

// DoProxy proxies an inbound request to its corresponding upstream origin with no caching features
func DoProxy(w io.Writer, r *http.Request, closeResponse bool) *http.Response {

	rsc := request.GetResources(r)
	o := rsc.BackendOptions

	start := time.Now()

	_, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "ProxyRequest")
	if span != nil {
		defer span.End()
	}

	pc := rsc.PathConfig

	var elapsed time.Duration
	var cacheStatusCode status.LookupStatus
	var resp *http.Response
	var reader io.ReadCloser

	if pc == nil || pc.CollapsedForwardingType != forwarding.CFTypeProgressive ||
		!methods.HasBody(r.Method) {
		reader, resp, _ = PrepareFetchReader(r)
		cacheStatusCode = setStatusHeader(resp.StatusCode, resp.Header)
		writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
		if writer != nil && reader != nil {
			io.Copy(writer, reader)
		}
	} else {
		pr := newProxyRequest(r, w)
		key := o.CacheKeyPrefix + "." + pr.DeriveCacheKey("")
		result, ok := reqs.Load(key)
		if !ok {
			var contentLength int64
			reader, resp, contentLength = PrepareFetchReader(r)
			cacheStatusCode = setStatusHeader(resp.StatusCode, resp.Header)
			pr.mapLock.Lock()
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			pr.mapLock.Unlock()
			// Check if we know the content length and if it is less than our max object size.
			if contentLength != 0 && contentLength < int64(o.MaxObjectSizeBytes) {
				pcf := NewPCF(resp, contentLength)
				reqs.Store(key, pcf)
				// Blocks until server completes
				grClose := reader != nil && closeResponse
				closeResponse = false
				go func() {
					io.Copy(pcf, reader)
					pcf.Close()
					reqs.Delete(key)
					if grClose {
						reader.Close()
					}
				}()
				if err := pcf.AddClient(writer); err != nil {
					return nil
				}
			}
		} else {
			pcf, _ := result.(ProgressiveCollapseForwarder)
			resp = pcf.GetResp()
			pr.mapLock.Lock()
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			pr.mapLock.Unlock()
			if err := pcf.AddClient(writer); err != nil {
				return nil
			}
		}
	}

	if closeResponse && reader != nil {
		reader.Close()
	}

	elapsed = time.Since(start)

	if resp != nil && rsc != nil && (rsc.IsMergeMember || rsc.TSTransformer != nil) {
		rsc.Response = resp
		recordResults(r, "HTTPProxy", cacheStatusCode, resp.StatusCode,
			r.URL.Path, "", elapsed.Seconds(), nil, resp.Header)
	}

	return resp
}

// PrepareResponseWriter prepares a response and returns a destination io.Writer for the payload
// Used in Respond.
func PrepareResponseWriter(w io.Writer, code int, header http.Header) io.Writer {
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		headers.Merge(h, header)
		headers.AddResponseHeaders(h)
		if code > 0 {
			rw.WriteHeader(code)
		}
		return rw
	}
	return w
}

// PrepareFetchReader prepares an http response and returns io.ReadCloser to
// provide the response data, the response object and the content length.
// Used in Fetch.
func PrepareFetchReader(r *http.Request) (io.ReadCloser, *http.Response, int64) {

	rsc := request.GetResources(r)

	ep := profile.FromContext(r.Context())

	o := rsc.BackendOptions

	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "PrepareFetchReader")
	if span != nil {
		defer span.End()
	}

	pc := rsc.PathConfig

	var rc io.ReadCloser

	headers.AddForwardingHeaders(r, o.ForwardedHeaders)
	// clear the Host header before proxying or it will be forwarded upstream
	r.Host = ""

	if pc != nil && len(pc.RequestHeaders) > 0 {
		headers.UpdateRequestHeaders(r, pc.RequestHeaders)
	}
	if pc != nil && len(pc.RequestParams) > 0 {
		qp, _, _ := params.GetRequestValues(r)
		params.UpdateParams(qp, pc.RequestParams)
		params.SetRequestValues(r, qp)
	}

	if ep := profile.FromContext(r.Context()); ep != nil && ep.SupportedHeaderVal != "" {
		r.Header.Set(headers.NameAcceptEncoding, ep.SupportedHeaderVal)
	}

	r.Close = false
	r.RequestURI = ""

	if rsc.Tracer != nil {
		// Processing traces for proxies
		// https://www.w3.org/TR/trace-context-1/#alternative-processing
		ctx, r = othttptrace.W3C(ctx, r)
		othttptrace.Inject(ctx, r)
	}

	_, doSpan := tspan.NewChildSpan(r.Context(), rsc.Tracer, "ProxyRequest")
	if doSpan != nil {
		defer doSpan.End()
	}

	if ep := profile.FromContext(r.Context()); ep != nil && ep.SupportedHeaderVal != "" {
		r.Header.Set(headers.NameAcceptEncoding, ep.SupportedHeaderVal)
	}

	resp, err := o.HTTPClient.Do(r)
	if err != nil {
		logger.Error("error downloading url",
			logging.Pairs{"url": r.URL.String(), "detail": err.Error()})
		// if there is an err and the response is nil, the server could not be reached
		// so make a 502 for the downstream response
		if resp == nil {
			resp = &http.Response{StatusCode: http.StatusBadGateway,
				Request: r, Header: make(http.Header)}
		}

		if pc != nil {
			headers.UpdateHeaders(resp.Header, pc.ResponseHeaders)
		}

		if doSpan != nil {
			doSpan.AddEvent(
				"Failure",
				trace.EventOption(trace.WithAttributes(
					attribute.String("error", err.Error()),
					attribute.Int("httpStatus", resp.StatusCode),
				)),
			)
			doSpan.SetStatus(tracing.HTTPToCode(resp.StatusCode), "")
		}
		return nil, resp, 0
	}

	if ce := resp.Header.Get(headers.NameContentEncoding); ep != nil && ce != "" {
		ep.ContentEncoding = ce
	}

	originalLen := int64(-1)
	if v, ok := resp.Header[headers.NameContentLength]; ok {
		originalLen, err = strconv.ParseInt(strings.Join(v, ""), 10, 64)
		if err != nil {
			originalLen = -1
		}
		resp.ContentLength = originalLen
	}

	// warn if the clock between trickster and the origin is off by more than 1 minute
	if date := resp.Header.Get(headers.NameDate); date != "" {
		d, err := http.ParseTime(date)
		if err == nil {
			if offset := time.Since(d); time.Duration(math.Abs(float64(offset))) > time.Minute {
				logger.WarnOnce("clockoffset."+o.Name,
					ClockOffsetWarning,
					logging.Pairs{
						"backendName":   o.Name,
						"tricksterTime": strconv.FormatInt(d.Add(offset).Unix(), 10),
						"originTime":    strconv.FormatInt(d.Unix(), 10),
						"offset":        strconv.FormatInt(int64(offset.Seconds()), 10) + "s",
					})
			}
		}
	}

	hasCustomResponseBody := false
	resp.Header.Del(headers.NameContentLength)

	if pc != nil {
		headers.UpdateHeaders(resp.Header, pc.ResponseHeaders)
		hasCustomResponseBody = pc.HasCustomResponseBody
	}

	if hasCustomResponseBody {
		// Since we are not responding with the actual upstream response body, close it here
		resp.Body.Close()
		rc = io.NopCloser(bytes.NewReader(pc.ResponseBodyBytes))
	} else {
		rc = resp.Body
	}

	return rc, resp, originalLen
}

// Respond sends an HTTP Response down to the requesting client
func Respond(w io.Writer, code int, header http.Header, body io.Reader) {
	PrepareResponseWriter(w, code, header)
	if body != nil {
		io.Copy(w, body)
	}
}

func setStatusHeader(httpStatus int, header http.Header) status.LookupStatus {
	st := status.LookupStatusProxyOnly
	if httpStatus >= http.StatusBadRequest {
		st = status.LookupStatusProxyError
	}
	headers.SetResultsHeader(header, "HTTPProxy", st.String(), "", nil)
	return st
}

func recordResults(r *http.Request, engine string, cacheStatus status.LookupStatus,
	statusCode int, path, ffStatus string, elapsed float64, extents timeseries.ExtentList,
	header http.Header) {

	rsc := request.GetResources(r)
	pc := rsc.PathConfig
	o := rsc.BackendOptions
	s := cacheStatus.String()

	if pc != nil && !pc.NoMetrics {
		httpStatus := strconv.Itoa(statusCode)
		lvs := []string{o.Name, o.Provider, r.Method, s, httpStatus, path}
		metrics.ProxyRequestStatus.WithLabelValues(lvs...).Inc()
		if elapsed > 0 {
			metrics.ProxyRequestDuration.WithLabelValues(lvs...).Observe(elapsed)
		}
	}
	headers.SetResultsHeader(header, engine, s, ffStatus, extents)
}
