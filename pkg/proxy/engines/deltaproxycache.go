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
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tc "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

const (
	statusOff  = "off"
	statusErr  = "err"
	statusHit  = "hit"
	statusMiss = "miss"

	// errorBodyCap bounds the amount of upstream error body copied into
	// HTTPDocument on non-2xx responses. Protects singleflight waiters
	// from a malicious or misconfigured origin that returns a huge error
	// page. 1 MiB is larger than any reasonable structured error payload.
	errorBodyCap = 1 << 20
)

// fetchFastForward executes a fast-forward request and merges the result into rts.
// Returns the fast-forward status string ("off", "hit", "miss", or "err").
func fetchFastForward(
	ctx context.Context, r *http.Request,
	o *bo.Options, cc *co.Options, cache tc.Cache,
	client backends.TimeseriesBackend, rsc *request.Resources,
	rlo *timeseries.RequestOptions, trq *timeseries.TimeRangeQuery,
	normalizedNow *timeseries.TimeRangeQuery, modeler *timeseries.Modeler,
	rts timeseries.Timeseries,
) string {
	if rlo.FastForwardDisable {
		return statusOff
	}
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	if trq.Step <= o.FastForwardTTL {
		return statusOff
	}
	ffReq, err := client.FastForwardRequest(r)
	if err != nil || ffReq == nil || ffReq.URL == nil || ffReq.URL.Scheme == "" {
		return statusErr
	}
	// Only fast forward if the user request is for the absolute latest datapoint
	if !trq.Extent.End.Equal(normalizedNow.Extent.End) {
		return statusOff
	}
	ffReq = ffReq.WithContext(profile.ToContext(ffReq.Context(), dpcEncodingProfile.Clone()))
	rs := request.NewResources(o, o.FastForwardPath, cc, cache, client, rsc.Tracer)
	rs.AlternateCacheTTL = o.FastForwardTTL
	ffReq = ffReq.WithContext(tctx.WithResources(ffReq.Context(), rs))

	_, ffSpan := tspan.NewChildSpan(ctx, rsc.Tracer, "FetchFastForward")
	if ffSpan != nil {
		ffReq = ffReq.WithContext(trace.ContextWithSpan(ffReq.Context(), ffSpan))
		defer ffSpan.End()
	}
	body, resp, isHit := FetchViaObjectProxyCache(ffReq)
	if resp == nil || resp.StatusCode != http.StatusOK || len(body) == 0 {
		return statusErr
	}
	ffts, err := modeler.WireUnmarshalerReader(getDecoderReader(resp), trq)
	if err != nil {
		logger.Error("proxy object unmarshaling failed", logging.Pairs{"body": string(body)})
		return statusErr
	}
	ffts.SetTimeRangeQuery(trq)
	x := ffts.Extents()
	ffStatus := statusMiss
	if isHit {
		ffStatus = statusHit
	}
	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was normalized to step boundaries and would remove fast forward data.
	// If the fast forward data point is older (e.g. cached) than the last datapoint in the
	// returned time series, it will not be merged
	if len(x) > 0 && x[0].End.After(trq.Extent.End) &&
		len(x) == 1 && x[0].Start.Truncate(time.Second).After(normalizedNow.Extent.End) {
		rts.Merge(false, ffts)
	}
	return ffStatus
}

// finalizeDPCResponse writes metrics, logs, and the HTTP response for a DPC request.
// If wireBody is non-nil, it is written directly (skipping marshal).
// Otherwise rts is marshaled to the wire format.
func finalizeDPCResponse(
	w http.ResponseWriter, r *http.Request, rsc *request.Resources,
	rts timeseries.Timeseries, rh http.Header, sc int,
	cacheStatus status.LookupStatus, ffStatus string, elapsed float64,
	missRanges timeseries.ExtentList, uncachedValueCount int64,
	key string, o *bo.Options, rlo *timeseries.RequestOptions,
	modeler *timeseries.Modeler, wireBody []byte,
) {
	dpStatus := logging.Pairs{
		"cacheKey":    key,
		"cacheStatus": cacheStatus,
		"reqStart":    rsc.TimeRangeQuery.Extent.Start.Unix(),
		"reqEnd":      rsc.TimeRangeQuery.Extent.End.Unix(),
	}
	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}
	cachedValueCount := rts.ValueCount() - uncachedValueCount
	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "cached", r.URL.Path).Add(float64(cachedValueCount))
	}

	// Respond to the user. Using the response headers from a Delta Response,
	// so as to not map conflict with cacheData on WriteCache
	logDeltaRoutine(dpStatus)
	recordDPCResult(r, cacheStatus, sc, r.URL.Path, ffStatus, elapsed, missRanges, rh)

	rsc.TS = rts
	Respond(w, 0, rh, nil) // body and code are nil so this only sets appropriate headers; no writes
	if rsc.TSTransformer != nil {
		rsc.TSTransformer(rts)
	}
	if rsc.IsMergeMember { // don't bother marshaling this dataset if it's just going to be merged internally
		if rsc.Response == nil {
			rsc.Response = &http.Response{StatusCode: sc}
		}
		return
	}
	if wireBody != nil {
		w.Write(wireBody)
	} else {
		modeler.WireMarshalWriter(rts, rlo, sc, w)
	}
}

// DeltaProxyCache is used for Time Series Acceleration, but not for normal HTTP Object Caching

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset to the downstream
// request while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(w http.ResponseWriter, r *http.Request, modeler *timeseries.Modeler) {
	rsc := request.GetResources(r)
	o := rsc.BackendOptions
	if o == nil || o.ProxyOnly {
		DoProxy(w, r, true)
		return
	}

	if modeler != nil {
		rsc.TSMarshaler = modeler.WireMarshalWriter
		rsc.TSUnmarshaler = modeler.WireUnmarshaler
	}
	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "DeltaProxyCacheRequest")
	if span != nil {
		defer span.End()
	}
	r = r.WithContext(ctx)

	pc := rsc.PathConfig
	cache := rsc.CacheClient
	cc := rsc.CacheConfig
	client := rsc.BackendClient.(backends.TimeseriesBackend)

	trq, rlo, canOPC, err := client.ParseTimeRangeQuery(r)
	rsc.Lock()
	rsc.TimeRangeQuery = trq
	rsc.TSReqestOptions = rlo
	rsc.Unlock()
	if err != nil {
		if canOPC {
			logger.Debug("could not parse time range query, using object proxy cache",
				logging.Pairs{"error": err.Error()})
			rsc.AlternateCacheTTL = time.Minute
			ObjectProxyCacheRequest(w, r)
			return
		}
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		if trq != nil && trq.OriginalBody != nil {
			request.SetBody(r, trq.OriginalBody)
		}
		DoProxy(w, r, true)
		return
	}
	var cacheStatus status.LookupStatus

	pr := newProxyRequest(r, w)
	rlo.FastForwardDisable = o.FastForwardDisable || rlo.FastForwardDisable
	trq.NormalizeExtent()
	now := time.Now()
	bt := trq.GetBackfillTolerance(o.BackfillTolerance, o.BackfillTolerancePoints)
	bfs := now.Add(-bt).Truncate(trq.Step) // start of the backfill tolerance window

	OldestRetainedTimestamp := time.Time{}
	if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodOldest {
		OldestRetainedTimestamp = now.Truncate(trq.Step).Add(-(trq.Step * o.TimeseriesRetention))
		if trq.Extent.End.Before(OldestRetainedTimestamp) {
			logger.Debug("timerange end is too old to consider caching",
				logging.Pairs{
					"oldestRetainedTimestamp": OldestRetainedTimestamp,
					"step":                    trq.Step, "retention": o.TimeseriesRetention,
				})
			if trq.OriginalBody != nil {
				request.SetBody(r, trq.OriginalBody)
			}
			DoProxy(w, r, true)
			return
		}
	}

	client.SetExtent(pr.upstreamRequest, trq, &trq.Extent)
	key := o.CacheKeyPrefix + ".dpc." + pr.DeriveCacheKey("")

	coReq := GetRequestCachingPolicy(r.Header)

	sfKey := key + "|" + strconv.FormatInt(trq.Extent.Start.UnixMilli(), 10) +
		"|" + strconv.FormatInt(trq.Extent.End.UnixMilli(), 10)

	// this is used to determine if Fast Forward should be activated for this request
	normalizedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: now},
		Step:   trq.Step,
	}
	normalizedNow.NormalizeExtent()

	var doc *HTTPDocument
	var elapsed time.Duration
	var rts timeseries.Timeseries
	var uncachedValueCount int64
	var missRanges timeseries.ExtentList

	if !coReq.NoCache {
		// it's not a NoCache request, so something is _likely_ going to be cached now.
		// we use singleflight here, so as to prevent other concurrent client requests for
		// the same url, which will have the same cacheStatus, from causing the same or
		// similar HTTP requests to be made against the origin, since just one should do.

		// isExecutor distinguishes the executor from waiters after Do returns,
		// since singleflight.Do returns shared=true for the executor too.
		var isExecutor bool
		v, sfErr, _ := dpcGroup.Do(sfKey, func() (any, error) {
			isExecutor = true
			// buildErrorResult constructs a dpcResult for error responses.
			buildErrorResult := func(sc int, h http.Header, body []byte) *dpcResult {
				return &dpcResult{
					statusCode:  sc,
					headers:     h,
					body:        body,
					elapsed:     float64(time.Since(now).Seconds()),
					cacheStatus: status.LookupStatusProxyError,
				}
			}

			var cts timeseries.Timeseries
			var doc *HTTPDocument
			var elapsed time.Duration
			var cacheStatus status.LookupStatus
			var missRanges, cvr timeseries.ExtentList

			doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil, modeler.CacheUnmarshaler)
			if cacheStatus == status.LookupStatusKeyMiss && errors.Is(err, tc.ErrKNF) {
				cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
				if err != nil {
					return buildErrorResult(doc.StatusCode, doc.SafeHeaderClone(), doc.Body), nil
				}
			} else {
				if doc == nil {
					err = tpe.ErrEmptyDocumentBody
				} else if doc.timeseries == nil {
					err = tpe.ErrEmptyDocumentBody
				}
				if err != nil {
					logger.Error("cache object unmarshaling failed",
						logging.Pairs{"key": key, "backendName": client.Name(), "detail": err.Error()})
					go cache.Remove(key)
					cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
					if err != nil {
						return buildErrorResult(doc.StatusCode, doc.SafeHeaderClone(), doc.Body), nil
					}
				} else {
					cts = doc.timeseries.Clone() // Load the Cached Timeseries
					if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodLRU {
						el := cts.Extents()
						tsc := cts.TimestampCount()
						if tsc > 0 && tsc >= int64(o.TimeseriesRetentionFactor) {
							if trq.Extent.End.Before(el[0].Start) {
								// too old to cache; return a sentinel so the caller proxies
								return &dpcResult{cacheStatus: status.LookupStatusProxyOnly}, nil
							}
						}
					}
					cacheStatus = status.LookupStatusPartialHit
				}
			}

			// Find the ranges that we want, but which are not currently cached
			var vr timeseries.ExtentList
			if cts != nil {
				vr = cts.VolatileExtents()
			}
			if cacheStatus == status.LookupStatusPartialHit {
				missRanges = cts.Extents().CalculateDeltas(timeseries.ExtentList{trq.Extent}, trq.Step)
				// this is the backfill part of backfill tolerance. if there are any volatile
				// ranges in the timeseries, this determines if any fall within the client's
				// requested range and ensures they are re-requested. this only happens if
				// the request is already a phit
				if bt > 0 && len(missRanges) > 0 && len(vr) > 0 {
					// this checks the timeseries's volatile ranges for any overlap with
					// the request extent, and adds those to the missRanges to refresh
					if cvr = vr.Crop(trq.Extent); len(cvr) > 0 {
						merged := make(timeseries.ExtentList, len(missRanges)+len(cvr))
						copy(merged, missRanges)
						copy(merged[len(missRanges):], cvr)
						missRanges = merged.Compress(trq.Step)
					}
				}
			}
			if len(missRanges) == 0 && cacheStatus == status.LookupStatusPartialHit {
				// on full cache hit, elapsed records the time taken to query the cache
				// and definitively conclude that it is a full cache hit
				elapsed = time.Since(now)
				cacheStatus = status.LookupStatusHit
			} else if len(missRanges) == 1 && missRanges[0].Start.Equal(trq.Extent.Start) &&
				missRanges[0].End.Equal(trq.Extent.End) {
				cacheStatus = status.LookupStatusRangeMiss
			}

			// this concurrently fetches all missing ranges from the origin
			if cacheStatus != status.LookupStatusHit && len(missRanges) > 0 {
				if o.DoesShard {
					missRanges = missRanges.Splice(trq.Step, o.MaxShardSizeTime, o.ShardStep, o.MaxShardSizePoints)
				}
				frsc := request.NewResources(o, pc, cc, cache, client, rsc.Tracer)
				frsc.TimeRangeQuery = trq
				var mts timeseries.List
				var mresp *http.Response
				fetchHeaders := http.Header(doc.Headers).Clone()
				mts, _, mresp, ferr := fetchExtents(missRanges, frsc,
					fetchHeaders, client, pr, modeler.WireUnmarshalerReader, span)
				if ferr != nil {
					return buildErrorResult(mresp.StatusCode, mresp.Header.Clone(),
						func() []byte { b, _ := io.ReadAll(mresp.Body); return b }()), nil
				}
				doc.Headers = fetchHeaders
				// Merge the new delta timeseries into the cached timeseries
				if len(mts) > 0 {
					// on phit, elapsed records the time spent waiting for all upstream requests to complete
					elapsed = time.Since(now)
					cts.Merge(true, mts...)
				}
			}

			// this handles the tolerance part of backfill tolerance, by adding new tolerable ranges to
			// the timeseries's volatile list, and removing those that no longer tolerate backfill
			if bt > 0 && cacheStatus != status.LookupStatusHit {
				var shouldCompress bool
				ve := cts.VolatileExtents()
				// first, remove those that are now too old to tolerate backfill.
				if len(cvr) > 0 {
					// this updates the timeseries's volatile list to remove anything just fetched that is
					// older than the current backfill tolerance timestamp; so it is now immutable in cache
					ve = ve.Remove(cvr, trq.Step)
					shouldCompress = true
				}
				// now add in any new time ranges that should tolerate backfill
				var adds timeseries.Extent
				if trq.Extent.End.After(bfs) {
					adds.End = trq.Extent.End
					if trq.Extent.Start.Before(bfs) {
						adds.Start = bfs
					} else {
						adds.Start = trq.Extent.Start
					}
				}
				if !adds.End.IsZero() {
					ve = append(ve, adds)
					shouldCompress = true
				}
				// if any changes happened to the volatile list, set it in the cached timeseries
				if shouldCompress {
					cts.SetVolatileExtents(ve.Compress(trq.Step))
				}
			}

			// cts is the cacheable time series, rts is the user's response timeseries
			var rts timeseries.Timeseries
			if cacheStatus != status.LookupStatusKeyMiss {
				rts = cts.CroppedClone(trq.Extent)
			} else {
				rts = cts.Clone()
			}

			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the
			// Backfill Tolerance before storing to cache
			if cacheStatus != status.LookupStatusHit {
				switch o.TimeseriesEvictionMethod {
				case evictionmethods.EvictionMethodLRU:
					cts.CropToSize(o.TimeseriesRetentionFactor, now, trq.Extent)
				default:
					cts.CropToRange(timeseries.Extent{End: now, Start: OldestRetainedTimestamp})
				}
				// Don't cache datasets with empty extents
				// (everything was cropped so there is nothing to cache)
				if len(cts.Extents()) > 0 {
					doc.timeseries = cts
					if werr := WriteCache(ctx, cache, key, doc, o.TimeseriesTTL,
						o.CompressibleTypes, modeler.CacheMarshaler); werr != nil {
						logger.Error("error writing object to cache",
							logging.Pairs{
								"backendName": o.Name,
								"cacheName":   cache.Configuration().Name,
								"cacheKey":    key,
								"detail":      werr.Error(),
							},
						)
					}
				}
			}

			uncachedValueCount := rts.ValueCount() - cts.ValueCount()

			ffStatus := fetchFastForward(ctx, r, o, cc, cache, client, rsc,
				rlo, trq, normalizedNow, modeler, rts)

			// marshal the response timeseries to wire format
			rts.SetExtents(nil) // so they are not included in the client response json
			var buf bytes.Buffer
			modeler.WireMarshalWriter(rts, rlo, doc.StatusCode, &buf)

			return &dpcResult{
				wireBody:           buf.Bytes(),
				rts:                rts,
				headers:            doc.SafeHeaderClone(),
				statusCode:         doc.StatusCode,
				elapsed:            float64(elapsed.Seconds()),
				ffStatus:           ffStatus,
				uncachedValueCount: uncachedValueCount,
				cacheStatus:        cacheStatus,
				missRanges:         missRanges,
			}, nil
		})

		if sfErr != nil {
			Respond(w, http.StatusBadGateway, http.Header{}, nil)
			return
		}

		result := v.(*dpcResult)

		// handle sentinel statuses that require special responses
		if result.cacheStatus == status.LookupStatusProxyOnly {
			// LRU eviction determined the request is too old to cache
			if trq.OriginalBody != nil {
				request.SetBody(r, trq.OriginalBody)
			}
			DoProxy(w, r, true)
			return
		}
		if result.cacheStatus == status.LookupStatusProxyError {
			rh := result.headers.Clone()
			recordDPCResult(r, status.LookupStatusProxyError, result.statusCode,
				r.URL.Path, "", result.elapsed, nil, rh)
			Respond(w, result.statusCode, rh, bytes.NewReader(result.body))
			return
		}

		cacheStatus = result.cacheStatus
		if !isExecutor {
			if status.IsSuccessful(cacheStatus) {
				cacheStatus = status.LookupStatusProxyHit
			} else {
				cacheStatus = status.LookupStatusProxyError
			}
		}

		tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", cacheStatus.String()))

		rh := result.headers.Clone()
		sc := result.statusCode

		// for merge members or requests with a TSTransformer, provide the timeseries
		if rsc.IsMergeMember || rsc.TSTransformer != nil {
			rts := result.rts.Clone()
			finalizeDPCResponse(w, r, rsc, rts, rh, sc,
				cacheStatus, result.ffStatus, result.elapsed, result.missRanges,
				result.uncachedValueCount, key, o, rlo, modeler, nil)
			return
		}

		// normal path: serve the pre-marshaled wire bytes directly
		finalizeDPCResponse(w, r, rsc, result.rts, rh, sc,
			cacheStatus, result.ffStatus, result.elapsed, result.missRanges,
			result.uncachedValueCount, key, o, rlo, modeler, result.wireBody)
		return
	}

	// noCache: bypass cache and singleflight, fetch directly from origin
	if span != nil {
		span.AddEvent("Not Caching")
	}
	cacheStatus = status.LookupStatusPurge
	go cache.Remove(key)
	var cts timeseries.Timeseries
	cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
	if err != nil {
		h := doc.SafeHeaderClone()
		recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
			r.URL.Path, "", elapsed.Seconds(), nil, h)
		Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
		return
	}
	rts = cts.Clone()

	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", cacheStatus.String()))

	ffStatus := fetchFastForward(ctx, r, o, cc, cache, client, rsc,
		rlo, trq, normalizedNow, modeler, rts)

	rts.SetExtents(nil) // so they are not included in the client response json
	rh := doc.SafeHeaderClone()
	sc := doc.StatusCode

	finalizeDPCResponse(w, r, rsc, rts, rh, sc,
		cacheStatus, ffStatus, elapsed.Seconds(), missRanges, uncachedValueCount,
		key, o, rlo, modeler, nil)
}

func logDeltaRoutine(p logging.Pairs) {
	logger.Debug("delta routine completed", p)
}

var dpcEncodingProfile = &profile.Profile{
	ClientAcceptEncoding: providers.AllSupportedWebProviders,
	Supported:            7,
	SupportedHeaderVal:   providers.AllSupportedWebProviders,
}

func fetchTimeseries(pr *proxyRequest, trq *timeseries.TimeRangeQuery,
	client backends.TimeseriesBackend, modeler *timeseries.Modeler) (timeseries.Timeseries,
	*HTTPDocument, time.Duration, error,
) {
	rsc := pr.rsc.Clone()
	o := rsc.BackendOptions
	pc := rsc.PathConfig

	var handlerName string
	if pc != nil {
		handlerName = pc.HandlerName
	}

	ctx, span := tspan.NewChildSpan(pr.upstreamRequest.Context(), rsc.Tracer, "FetchTimeSeries")
	if span != nil {
		defer span.End()
	}

	ctx = profile.ToContext(ctx, dpcEncodingProfile.Clone())
	pr.upstreamRequest = request.SetResources(pr.upstreamRequest.WithContext(ctx), rsc)

	start := time.Now()
	mts, _, resp, err := fetchExtents(timeseries.ExtentList{trq.Extent}.Splice(trq.Step,
		o.MaxShardSizeTime, o.ShardStep, o.MaxShardSizePoints), rsc,
		http.Header{}, client, pr, modeler.WireUnmarshalerReader, nil)

	// elaspsed measures only the time spent making origin requests
	var elapsed time.Duration
	if err == nil {
		elapsed = time.Since(start)
	}

	go logUpstreamRequest(o.Name, o.Provider, handlerName,
		pr.Method, pr.URL.String(), pr.UserAgent(), resp.StatusCode, 0, elapsed.Seconds())

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}

	if err != nil {
		// Capture the upstream error body so collapsed singleflight waiters
		// and negative-cache entries see the origin's error detail instead
		// of an empty body. fetchExtents already read the body into a
		// bytes.NewReader wrapper (deltaproxycache.go:720-727), so reading
		// again is safe; we still cap with io.LimitReader as a belt-and-
		// suspenders guard against pathological origin responses.
		if resp.Body != nil {
			b, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyCap))
			if readErr == nil && len(b) > 0 {
				d.Body = b
			}
		}
		return nil, d, time.Duration(0), err
	}

	var ts timeseries.Timeseries
	if len(mts) == 1 {
		ts = mts[0]
	} else if len(mts) > 1 {
		ts = mts[0]
		ts.Merge(true, mts[1:]...)
	}

	return ts, d, elapsed, nil
}

func recordDPCResult(r *http.Request, cacheStatus status.LookupStatus,
	httpStatus int, path, ffStatus string, elapsed float64,
	needed timeseries.ExtentList, header http.Header,
) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus,
		elapsed, needed, header)
}

func getDecoderReader(resp *http.Response) io.Reader {
	var reader io.Reader = resp.Body
	// if the content is encoded, it will need to be decoded
	if ce := resp.Header.Get(headers.NameContentEncoding); ce != "" {
		decoderInit := providers.GetDecoderInitializer(ce)
		if decoderInit != nil {
			reader = decoderInit(io.NopCloser(reader))
			resp.Header.Del(headers.NameContentEncoding)
		}
	}
	return reader
}

// this will concurrently fetch provided requested extents
func fetchExtents(el timeseries.ExtentList, rsc *request.Resources, h http.Header,
	client backends.TimeseriesBackend, pr *proxyRequest, wur timeseries.UnmarshalerReaderFunc,
	span trace.Span,
) (timeseries.List, int64, *http.Response, error) {
	var uncachedValueCount atomic.Int64
	var appendLock, respLock sync.Mutex
	errs := make([]error, len(el))

	// the list of time series created from the responses
	mts := make(timeseries.List, len(el))
	// the meta-response aggregating all upstream responses
	mresp := &http.Response{Header: h}

	// limit concurrent upstream requests to avoid overwhelming the origin
	eg := errgroup.Group{}
	limit := bo.DefaultFetchConcurrencyLimit
	if rsc.BackendOptions != nil && rsc.BackendOptions.FetchConcurrencyLimit > 0 {
		limit = rsc.BackendOptions.FetchConcurrencyLimit
	}
	eg.SetLimit(limit)

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range el {
		// This concurrently fetches gaps from the origin and adds their datasets to the merge list
		eg.Go(func() error {
			e := &el[i]
			rq := pr.Clone()
			mrsc := rsc.Clone()
			rq.upstreamRequest = rq.upstreamRequest.WithContext(tctx.WithResources(
				trace.ContextWithSpan(context.Background(), span),
				mrsc))
			rq.upstreamRequest = rq.upstreamRequest.WithContext(profile.ToContext(rq.upstreamRequest.Context(),
				dpcEncodingProfile.Clone()))
			client.SetExtent(rq.upstreamRequest, rsc.TimeRangeQuery, e)

			ctxMR, spanMR := tspan.NewChildSpan(rq.upstreamRequest.Context(), rsc.Tracer, "FetchRange")
			if spanMR != nil {
				rq.upstreamRequest = rq.upstreamRequest.WithContext(ctxMR)
				defer spanMR.End()
			}

			body, resp, _ := rq.Fetch()

			respLock.Lock()
			if resp.StatusCode > mresp.StatusCode {
				mresp.Status = resp.Status
				mresp.StatusCode = resp.StatusCode
			}
			respLock.Unlock()

			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, ferr := wur(getDecoderReader(resp), rsc.TimeRangeQuery)
				if ferr != nil {
					logger.Error("proxy object unmarshaling failed",
						logging.Pairs{"detail": ferr.Error()})
					errs[i] = ferr
					return nil
				}
				uncachedValueCount.Add(nts.ValueCount())
				nts.SetTimeRangeQuery(rsc.TimeRangeQuery)
				nts.SetExtents(timeseries.ExtentList{*e})
				appendLock.Lock()
				headers.Merge(h, resp.Header)
				appendLock.Unlock()
				mts[i] = nts
			} else if resp.StatusCode != http.StatusOK {
				errs[i] = tpe.ErrUnexpectedUpstreamResponse
				var b []byte
				var s string
				if resp.Body != nil {
					var readErr error
					b, readErr = io.ReadAll(resp.Body)
					if readErr != nil {
						logger.Warn("failed to read upstream error response body",
							logging.Pairs{"detail": readErr.Error()})
					}
					s = string(b)
					respLock.Lock()
					mresp.Body = io.NopCloser(bytes.NewReader(b))
					respLock.Unlock()
				}
				if len(s) > 128 {
					s = s[:128]
				}
				logger.Error("unexpected upstream response",
					logging.Pairs{
						"statusCode":              resp.StatusCode,
						"clientRequestURL":        pr.Request.URL.String(),
						"clientRequestMethod":     pr.Request.Method,
						"clientRequestHeaders":    headers.SanitizeForLogging(pr.Request.Header),
						"upstreamRequestURL":      pr.upstreamRequest.URL.String(),
						"upstreamRequestMethod":   pr.upstreamRequest.Method,
						"upstreamRequestHeaders":  headers.LogString(pr.upstreamRequest.Header),
						"upstreamResponseHeaders": headers.LogString(resp.Header),
						"upstreamResponseBody":    s,
					},
				)
			}
			return nil
		})
	}
	eg.Wait()
	return mts, uncachedValueCount.Load(), mresp, errors.Join(errs...)
}
