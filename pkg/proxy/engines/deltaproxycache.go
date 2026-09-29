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
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

const (
	statusOff = "off"
	statusErr = "err"

	hnClickHouseFormat = "X-ClickHouse-Format"

	// errorBodyCap bounds the amount of upstream error body copied into
	// HTTPDocument on non-2xx responses. Protects singleflight waiters
	// from a malicious or misconfigured origin that returns a huge error
	// page. 1 MiB is larger than any reasonable structured error payload.
	errorBodyCap = 1 << 20
)

func dpcProxyErrorStatusCode(statusCode int) int {
	if statusCode == 0 || (statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices) {
		return http.StatusInternalServerError
	}
	return statusCode
}

func fetchLivePoint(r *http.Request, o *bo.Options, client backends.TimeseriesBackend,
	rlo *timeseries.RequestOptions, trq, alignedNow *timeseries.TimeRangeQuery, rts timeseries.Timeseries,
) string {
	// Fast Forward: an instant query's latest point, fetched as the provider's live partial bucket and
	// merged into rts; the return is the ffstatus: off, hit, kmiss or err
	if rlo.FastForwardDisable || trq.SampleModel != timeseries.SampleModelInstant {
		return statusOff
	}
	// if the step resolution <= partial_bucket_ttl, then no need to even try Fast Forward
	if trq.Step <= time.Duration(o.PartialBucketTTL) {
		return statusOff
	}
	// only a request for the absolute latest datapoint is fast forwarded
	if !trq.Extent.End.Equal(alignedNow.Extent.End) {
		return statusOff
	}
	pb := timeseries.PartialBucket{
		Label: alignedNow.Extent.End, Lower: alignedNow.Extent.End, Upper: trq.Requested.End,
		Edge: timeseries.BucketEdgeEnd,
	}
	ffts, st, err := client.FetchPartialBucket(r, trq, pb, true)
	if err != nil || ffts == nil {
		return statusErr
	}
	ffts.SetTimeRangeQuery(trq)
	x := ffts.Extents()
	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was aligned to step boundaries and would remove fast forward data.
	// If the fast forward data point is older (e.g. cached) than the last datapoint in the
	// returned time series, it will not be merged
	if len(x) > 0 && x[0].End.After(trq.Extent.End) &&
		len(x) == 1 && x[0].Start.Truncate(time.Second).After(alignedNow.Extent.End) {
		rts.Merge(false, ffts)
	}
	return st.String()
}

// prepareDPCResponse validates before cache or client writes. When fast-forward
// cannot change the data and rendering is request-independent, keep the bytes
// instead of discarding a complete serialization and repeating it later.
func prepareDPCResponse(rts timeseries.Timeseries, rlo *timeseries.RequestOptions,
	modeler *timeseries.Modeler, statusCode int,
) ([]byte, error) {
	if !rlo.FallbackToProxyOnError {
		return nil, nil
	}
	if !rlo.FastForwardDisable || rlo.MarshalVariesByRequest {
		return nil, modeler.WireMarshalWriter(rts, rlo, statusCode, io.Discard)
	}
	extents := rts.Extents()
	rts.SetExtents(nil)
	var buf bytes.Buffer
	err := modeler.WireMarshalWriter(rts, rlo, statusCode, &buf)
	rts.SetExtents(extents)
	if err != nil {
		return nil, err
	}
	body := buf.Bytes()
	if body == nil {
		body = []byte{}
	}
	return body, nil
}

// finalizeDPCResponse writes metrics, logs, and the HTTP response for a DPC request.
// If wireBody is non-nil, it is written directly (skipping marshal).
// Otherwise rts is marshaled to the wire format.
func finalizeDPCResponse(
	w http.ResponseWriter, r *http.Request, rsc *request.Resources,
	rts timeseries.Timeseries, rh http.Header, sc int,
	cacheStatus status.LookupStatus, ffStatus string, elapsed float64,
	missRanges, failed timeseries.ExtentList, uncachedValueCount int64,
	key string, o *bo.Options, rlo *timeseries.RequestOptions,
	modeler *timeseries.Modeler, wireBody []byte, partials []headers.PartialBucketResult,
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
	rh = setResponseFormat(rh, rlo)
	recordDPCResult(r, cacheStatus, sc, r.URL.Path, ffStatus, elapsed, missRanges, failed, rh, partials...)

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

func setResponseFormat(rh http.Header, rlo *timeseries.RequestOptions) http.Header {
	// the request options can name the content type and encoding of a marshaled body
	if rlo == nil || (rlo.ResponseContentType == "" && rlo.ResponseContentEncoding == "") {
		return rh
	}
	if rh == nil {
		rh = make(http.Header)
	}
	if rlo.ResponseContentType != "" {
		rh.Set(headers.NameContentType, rlo.ResponseContentType)
	}
	if rlo.ResponseContentEncoding != "" {
		rh.Set(headers.NameContentEncoding, rlo.ResponseContentEncoding)
	}
	return rh
}

// DeltaProxyCache is used for Time Series Acceleration, but not for normal HTTP Object Caching

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset to the downstream
// request while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(w http.ResponseWriter, r *http.Request, modeler *timeseries.Modeler) {
	rsc := request.GetResources(r)
	if modeler != nil {
		rsc.TSMarshaler = modeler.WireMarshalWriter
		rsc.TSUnmarshaler = modeler.WireUnmarshaler
	}
	o := rsc.BackendOptions
	if o == nil {
		DoProxy(w, r, true)
		return
	}
	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "DeltaProxyCacheRequest")
	if span != nil {
		defer span.End()
	}
	setResourceSpanAttributes(rsc, span)
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
		if o.ProxyOnly {
			if trq != nil && trq.OriginalBody != nil {
				request.SetBody(r, trq.OriginalBody)
			}
			DoProxy(w, r, true)
			return
		}
		if canOPC {
			logger.Debug("could not parse time range query, using object proxy cache",
				logging.Pairs{keys.Error: err.Error()})
			rsc.AlternateCacheTTL, rsc.PerCredentialCache = time.Minute, true
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
	if o.ProxyOnly {
		if trq.OriginalBody != nil {
			request.SetBody(r, trq.OriginalBody)
		}
		DoProxy(w, r, true)
		return
	}
	resolveStepAlignment(ctx, o, trq, rsc.Tracer, span)
	if trq.StepAlignment == timeseries.StepAlignmentOff {
		serveUnaligned(w, r, rsc, trq, rlo, modeler, timeseries.StepAlignmentOffTTL)
		return
	}
	now := time.Now()
	// the time series cache serves the interior and the partial buckets at its edges are fetched apart;
	// trq is republished so concurrent readers see the plan atomically
	rsc.Lock()
	full := trq.PlanEdges(now)
	rsc.TimeRangeQuery = trq
	rsc.Unlock()
	if !full {
		// a range with no complete bucket is all partial buckets, so the origin's answer comes through
		// the object proxy cache; an instant range here is drop's, with no grid instant
		var instants *timeseries.Extent
		if trq.SampleModel == timeseries.SampleModelInstant {
			e := trq.Extent
			instants = &e
		}
		serveAsSent(w, r, rsc, trq, rlo, modeler, time.Duration(o.PartialBucketTTL), instants)
		return
	}
	var cacheStatus status.LookupStatus

	pr := newProxyRequest(r, w)
	// Fast Forward is the live end of partial_end, so a resolved mode with no partial end skips it
	if _, end := trq.StepAlignment.Edges(); trq.StepAlignment != 0 && end != timeseries.EdgePartial {
		rlo.FastForwardDisable = true
	}
	rlo.FastForwardDisable = o.FastForwardDisable || rlo.FastForwardDisable
	// providers whose marshaling depends on parameters outside the cache key
	// must not share one pre-marshaled body across singleflight waiters
	marshalVaries := rlo.MarshalVariesByRequest
	// bfs is the start of the backfill tolerance window, on the query's grid
	bt := trq.GetBackfillTolerance(time.Duration(o.VolatileWindow), o.VolatileWindowPoints)
	bfs := timeseries.FloorToGrid(now.Add(-bt), trq.Step, trq.Phase)

	OldestRetainedTimestamp := time.Time{}
	if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodOldest {
		retentionStep := trq.CachePolicyStep()
		OldestRetainedTimestamp = oldestRetained(trq, int64(o.TimeseriesRetention), now)
		if trq.Extent.End.Before(OldestRetainedTimestamp) {
			logger.Debug("timerange end is too old to consider caching",
				logging.Pairs{
					"oldestRetainedTimestamp": OldestRetainedTimestamp,
					"step":                    retentionStep, "retention": o.TimeseriesRetention,
				})
			if trq.OriginalBody != nil {
				request.SetBody(r, trq.OriginalBody)
			}
			DoProxy(w, r, true)
			return
		}
	} else {
		// LRU eviction bounds the entry by bucket count rather than age, so a
		// wider request is cropped on store and refetched on every request
		metrics.ObserveTimeseriesRetentionFactor(o.Name,
			timeseries.ExtentList{trq.Extent}.TimestampCount(trq.CachePolicyStep()),
			o.TimeseriesRetentionFactor)
	}

	if err := client.SetExtent(pr.upstreamRequest, trq, &trq.Extent); err != nil {
		logger.Error("could not rewrite time range query",
			logging.Pairs{keys.Error: err.Error(), keys.BackendName: client.Name()})
		failures.HandleInternalServerError(w, r)
		return
	}
	key := ComposeCacheKey(o.Name, o.CacheKeyPrefix, "dpc", pr.DeriveCacheKey(""))

	coReq := GetRequestCachingPolicy(r.Header)

	sfKey := key + "|" + strconv.FormatInt(trq.Extent.Start.UnixMilli(), 10) +
		"|" + strconv.FormatInt(trq.Extent.End.UnixMilli(), 10)

	// this is used to determine if Fast Forward should be activated for this request
	alignedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: now},
		Step:   trq.Step,
	}
	alignedNow.AlignExtent()

	var doc *HTTPDocument
	var elapsed time.Duration
	var rts timeseries.Timeseries
	var uncachedValueCount int64
	var missRanges timeseries.ExtentList

	partials := startPartialBuckets(r, o, client, trq, now)
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
			buildErrorResult := func(sc int, h http.Header, body []byte, fext timeseries.ExtentList) *dpcResult {
				return &dpcResult{
					statusCode:    dpcProxyErrorStatusCode(sc),
					headers:       h,
					body:          body,
					elapsed:       float64(time.Since(now).Seconds()),
					cacheStatus:   status.LookupStatusProxyError,
					failedExtents: fext,
				}
			}

			var cts timeseries.Timeseries
			var doc *HTTPDocument
			var elapsed time.Duration
			var cacheStatus status.LookupStatus
			var missRanges, cvr timeseries.ExtentList
			var failedExts timeseries.ExtentList
			var severeFault bool

			doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil, modeler.CacheUnmarshaler)
			if cacheStatus == status.LookupStatusKeyMiss && errors.Is(err, tc.ErrKNF) {
				cts, doc, elapsed, failedExts, severeFault = fetchTimeseries(pr, trq, client, modeler, partials.sharedLimiter())
				if rlo.FallbackToProxyOnError && len(failedExts) > 0 {
					return &dpcResult{cacheStatus: status.LookupStatusProxyOnly}, nil
				}
				if len(failedExts) > 0 && severeFault {
					return buildErrorResult(doc.StatusCode, doc.SafeHeaderClone(), doc.Body, failedExts), nil
				}
			} else {
				if doc == nil || doc.timeseries == nil {
					err = tpe.ErrEmptyDocumentBody
				}
				if err != nil {
					logger.Error("cache object unmarshaling failed",
						logging.Pairs{keys.Key: key, keys.BackendName: client.Name(), keys.Detail: err.Error()})
					goWithRecover("dpc.cache.Remove.unmarshal", func() { cache.Remove(key) })
					cts, doc, elapsed, failedExts, severeFault = fetchTimeseries(pr, trq, client, modeler, partials.sharedLimiter())
					if rlo.FallbackToProxyOnError && len(failedExts) > 0 {
						return &dpcResult{cacheStatus: status.LookupStatusProxyOnly}, nil
					}
					if len(failedExts) > 0 && severeFault {
						return buildErrorResult(doc.StatusCode, doc.SafeHeaderClone(), doc.Body, failedExts), nil
					}
					// entry was removed and data came from origin; don't inherit the pre-recovery status
					cacheStatus = status.LookupStatusKeyMiss
				} else {
					cts = doc.timeseries.Clone() // Load the Cached Timeseries
					if trq.PolicyStep > 0 {
						// Raw-sample cache identity does not include the caller's policy hint.
						cts.SetTimeRangeQuery(trq)
					}
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
				missRanges = gridExtents(cts.Extents(), rsc).CalculateDeltas(
					timeseries.ExtentList{trq.Extent}, trq.Step)
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
					missRanges = missRanges.Splice(trq.Step, trq.Phase,
						time.Duration(o.MaxShardSizeTime), time.Duration(o.ShardStep),
						o.MaxShardSizePoints)
				}
				frsc := request.NewResources(o, pc, cc, cache, client, rsc.Tracer)
				frsc.TimeRangeQuery = trq
				frsc.TSReqestOptions = rlo
				var mts timeseries.List
				var mresp *http.Response

				fetchHeaders := http.Header(doc.Headers).Clone()
				mts, _, mresp, failedExts, severeFault = fetchExtents(missRanges, frsc,
					fetchHeaders, client, pr, modeler.WireUnmarshalerReader, span, partials.sharedLimiter())
				if rlo.FallbackToProxyOnError && len(failedExts) > 0 {
					return &dpcResult{cacheStatus: status.LookupStatusProxyOnly}, nil
				}
				if len(failedExts) > 0 && severeFault {
					// mresp.Body is only set inside fetchExtents's non-200
					// branch; when every shard fails at the transport level
					// (e.g. dial refused) mresp.Body remains nil and
					// io.ReadAll(nil) panics on the first Read.
					var body []byte
					if mresp != nil && mresp.Body != nil {
						body, _ = io.ReadAll(mresp.Body)
					}
					return buildErrorResult(mresp.StatusCode, mresp.Header.Clone(), body, failedExts), nil
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
			rts.SetTimeRangeQuery(trq)
			wireBody, err := prepareDPCResponse(rts, rlo, modeler, doc.StatusCode)
			if err != nil {
				return &dpcResult{cacheStatus: status.LookupStatusProxyOnly}, nil
			}

			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the
			// Backfill Tolerance before storing to cache
			if cacheStatus != status.LookupStatusHit {
				// a bucket still aggregating is served but never cached, so only complete
				// buckets reach the cache
				cacheEnd, bucketed := trq.LastCompleteLabel(now)
				switch o.TimeseriesEvictionMethod {
				case evictionmethods.EvictionMethodLRU:
					cts.CropToSize(o.TimeseriesRetentionFactor, now, trq.Extent)
					if x := cts.Extents(); bucketed && len(x) > 0 {
						cts.CropToRange(timeseries.Extent{Start: x[0].Start, End: cacheEnd})
					}
				default:
					if !bucketed {
						cacheEnd = now
					}
					cts.CropToRange(timeseries.Extent{End: cacheEnd, Start: OldestRetainedTimestamp})
				}
				// Don't cache datasets with empty extents
				// (everything was cropped so there is nothing to cache)
				if len(cts.Extents()) > 0 {
					doc.timeseries = cts
					if werr := WriteCache(ctx, cache, key, doc, time.Duration(o.TimeseriesTTL),
						o.CompressibleTypes, modeler.CacheMarshaler); werr != nil {
						logger.Error("error writing object to cache",
							logging.Pairs{
								keys.BackendName: o.Name,
								keys.CacheName:   cache.Configuration().Name,
								"cacheKey":       key,
								keys.Detail:      werr.Error(),
							},
						)
					}
				}
			}

			uncachedValueCount := rts.ValueCount() - cts.ValueCount()

			ffStatus := fetchLivePoint(r, o, client, rlo, trq, alignedNow, rts)

			// marshal the response timeseries to wire format, unless the
			// provider renders per request (see MarshalVariesByRequest), in
			// which case each caller marshals the shared timeseries itself
			rts.SetExtents(nil) // so they are not included in the client response json
			if wireBody == nil && !marshalVaries {
				var buf bytes.Buffer
				modeler.WireMarshalWriter(rts, rlo, doc.StatusCode, &buf)
				wireBody = buf.Bytes()
			}

			return &dpcResult{
				wireBody:           wireBody,
				rts:                rts,
				headers:            doc.SafeHeaderClone(),
				statusCode:         doc.StatusCode,
				elapsed:            float64(elapsed.Seconds()),
				ffStatus:           ffStatus,
				uncachedValueCount: uncachedValueCount,
				cacheStatus:        cacheStatus,
				missRanges:         missRanges,
				failedExtents:      failedExts,
			}, nil
		})

		if sfErr != nil {
			partials.stop()
			Respond(w, http.StatusBadGateway, http.Header{}, nil)
			return
		}

		result := v.(*dpcResult)

		// handle sentinel statuses that require special responses
		if result.cacheStatus == status.LookupStatusProxyOnly {
			// Retention or provider response validation requires the original query.
			partials.stop()
			if trq.OriginalBody != nil {
				request.SetBody(r, trq.OriginalBody)
			}
			DoProxy(w, r, true)
			return
		}
		if result.cacheStatus == status.LookupStatusProxyError {
			partials.stop()
			rh := result.headers.Clone()
			recordDPCResult(r, status.LookupStatusProxyError, result.statusCode,
				r.URL.Path, "", result.elapsed, nil, result.failedExtents, rh)
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

		if partials != nil {
			// the partial buckets are this caller's own, so they join its own copy of the interior
			rts := result.rts.Clone()
			pbs, values := partials.mergeInto(rts, o, now, rsc.Tracer, span)
			finalizeDPCResponse(w, r, rsc, rts, rh, sc,
				cacheStatus, result.ffStatus, result.elapsed, result.missRanges,
				result.failedExtents, result.uncachedValueCount+values, key, o, rlo, modeler, nil, pbs)
			return
		}

		// for merge members, requests with a TSTransformer, and providers
		// that render per request, provide the timeseries rather than the
		// executor's pre-marshaled body
		if rsc.IsMergeMember || rsc.TSTransformer != nil || marshalVaries {
			rts := result.rts
			if rsc.TSTransformer != nil || (rsc.IsMergeMember && !marshalVaries) {
				rts = result.rts.Clone()
			}
			finalizeDPCResponse(w, r, rsc, rts, rh, sc,
				cacheStatus, result.ffStatus, result.elapsed, result.missRanges,
				result.failedExtents, result.uncachedValueCount, key, o, rlo, modeler, nil, nil)
			return
		}

		// normal path: serve the pre-marshaled wire bytes directly
		finalizeDPCResponse(w, r, rsc, result.rts, rh, sc,
			cacheStatus, result.ffStatus, result.elapsed, result.missRanges,
			result.failedExtents, result.uncachedValueCount, key, o, rlo, modeler, result.wireBody, nil)
		return
	}

	// noCache: bypass cache and singleflight, fetch directly from origin
	if span != nil {
		span.AddEvent("Not Caching")
	}
	cacheStatus = status.LookupStatusPurge
	goWithRecover("dpc.cache.Remove.purge", func() { cache.Remove(key) })
	var cts timeseries.Timeseries
	var failedExts timeseries.ExtentList
	var severeFault bool

	cts, doc, elapsed, failedExts, severeFault = fetchTimeseries(pr, trq, client, modeler, partials.sharedLimiter())
	if rlo.FallbackToProxyOnError && len(failedExts) > 0 {
		partials.stop()
		if trq.OriginalBody != nil {
			request.SetBody(r, trq.OriginalBody)
		}
		DoProxy(w, r, true)
		return
	}
	if len(failedExts) > 0 && severeFault {
		partials.stop()
		h := doc.SafeHeaderClone()
		sc := dpcProxyErrorStatusCode(doc.StatusCode)
		recordDPCResult(r, status.LookupStatusProxyError, sc,
			r.URL.Path, "", elapsed.Seconds(), nil, failedExts, h)
		Respond(w, sc, h, bytes.NewReader(doc.Body))
		return
	}
	rts = cts.Clone()
	rts.SetTimeRangeQuery(trq)
	wireBody, err := prepareDPCResponse(rts, rlo, modeler, doc.StatusCode)
	if err != nil {
		partials.stop()
		if trq.OriginalBody != nil {
			request.SetBody(r, trq.OriginalBody)
		}
		DoProxy(w, r, true)
		return
	}

	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", cacheStatus.String()))

	ffStatus := fetchLivePoint(r, o, client, rlo, trq, alignedNow, rts)

	rts.SetExtents(nil) // so they are not included in the client response json
	pbs, values := partials.mergeInto(rts, o, now, rsc.Tracer, span)
	rh := doc.SafeHeaderClone()
	sc := doc.StatusCode
	// a transformer or this caller's partial buckets change rts after its body was marshaled
	if rsc.TSTransformer != nil || partials != nil {
		wireBody = nil
	}

	finalizeDPCResponse(w, r, rsc, rts, rh, sc,
		cacheStatus, ffStatus, elapsed.Seconds(), missRanges, failedExts, uncachedValueCount+values,
		key, o, rlo, modeler, wireBody, pbs)
}

func oldestRetained(trq *timeseries.TimeRangeQuery, retention int64, now time.Time) time.Time {
	// retention counts whole policy steps back from the current one, and the cutoff then
	// lands on the query's grid so crops never split a bucket
	policyStep := trq.CachePolicyStep()
	cutoff := timeseries.FloorToGrid(now, policyStep, trq.Phase).
		Add(-policyStep * time.Duration(retention))
	return timeseries.FloorToGrid(cutoff, trq.Step, trq.Phase)
}

func gridFetchExtent(e timeseries.Extent, rsc *request.Resources) (timeseries.Extent, bool) {
	trq := rsc.TimeRangeQuery
	if trq == nil || trq.Step <= 0 || (timeseries.OnGrid(e.Start, trq.Step, trq.Phase) &&
		timeseries.OnGrid(e.End, trq.Step, trq.Phase)) {
		return e, true
	}
	// a bound between buckets would render a partial bucket that then merges into the cache as
	// though it were complete, so only the whole buckets within the range are fetched
	observeOffGridExtents(rsc, 1, e.String())
	return e.ClampToGrid(trq.Step, trq.Phase)
}

func gridExtents(el timeseries.ExtentList, rsc *request.Resources) timeseries.ExtentList {
	trq := rsc.TimeRangeQuery
	if trq == nil {
		return el
	}
	// coverage recorded off the grid leaves holes that no delta would refetch, so it is
	// narrowed to the whole buckets it holds before the deltas are calculated
	out, offGrid := el.ClampToGrid(trq.Step, trq.Phase)
	if offGrid > 0 {
		observeOffGridExtents(rsc, offGrid, el.String())
	}
	return out
}

func observeOffGridExtents(rsc *request.Resources, count int, detail string) {
	var backendName, provider string
	if o := rsc.BackendOptions; o != nil {
		backendName, provider = o.Name, o.Provider
	}
	metrics.TimeseriesOffGridExtents.WithLabelValues(backendName, provider).Add(float64(count))
	logger.Debug("narrowed off-grid extents to whole buckets",
		logging.Pairs{keys.BackendName: backendName, keys.Extent: detail})
}

func logDeltaRoutine(p logging.Pairs) {
	logger.Debug("delta routine completed", p)
}

var dpcEncodingProfile = &profile.Profile{
	ClientAcceptEncoding: providers.AllSupportedWebProviders,
	Supported:            7,
	SupportedHeaderVal:   providers.AllSupportedWebProviders,
}

func dpcUpstreamEncodingProfile(rlo *timeseries.RequestOptions) *profile.Profile {
	ep := dpcEncodingProfile.Clone()
	if rlo != nil && rlo.UpstreamAcceptEncoding != "" {
		ep.ClientAcceptEncoding = rlo.UpstreamAcceptEncoding
		ep.SupportedHeaderVal = rlo.UpstreamAcceptEncoding
		// The provider, not the HTTP encoding layer, handles this wire format.
		ep.Supported = 0
	}
	return ep
}

func fetchTimeseries(
	pr *proxyRequest,
	trq *timeseries.TimeRangeQuery,
	client backends.TimeseriesBackend,
	modeler *timeseries.Modeler,
	limiter fetchLimiter,
) (timeseries.Timeseries, *HTTPDocument, time.Duration, timeseries.ExtentList, bool) {
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
	setResourceSpanAttributes(rsc, span)

	ctx = profile.ToContext(ctx, dpcUpstreamEncodingProfile(rsc.TSReqestOptions))
	pr.upstreamRequest = request.SetResources(pr.upstreamRequest.WithContext(ctx), rsc)

	start := time.Now()
	mts, _, resp, failedExts, faultStatus := fetchExtents(timeseries.ExtentList{trq.Extent}.Splice(trq.Step, trq.Phase,
		time.Duration(o.MaxShardSizeTime), time.Duration(o.ShardStep), o.MaxShardSizePoints), rsc,
		http.Header{}, client, pr, modeler.WireUnmarshalerReader, nil, limiter)
	if resp != nil {
		setHTTPStatusSpanAttributes(rsc.Tracer, resp.StatusCode, span)
	}

	// elaspsed measures only the time spent making origin requests
	var elapsed time.Duration
	if !faultStatus {
		elapsed = time.Since(start)
	}

	// A fallback may reuse and mutate the request after this function returns.
	method, target, userAgent := pr.Method, pr.URL.String(), pr.UserAgent()
	goWithRecover("dpc.logUpstreamRequest", func() {
		logUpstreamRequest(o.Name, o.Provider, handlerName,
			method, target, userAgent, resp.StatusCode, 0, elapsed.Seconds())
	})

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}

	if len(failedExts) > 0 && faultStatus {
		// Capture the upstream error body so collapsed singleflight waiters
		// and negative-cache entries see the origin's error detail instead
		// of an empty body.
		//
		// fetchExtents populates resp.Body (wrapped in a fresh
		// bytes.NewReader under respLock) only on the non-2xx response
		// path; see the ErrUnexpectedUpstreamResponse branch there. On the
		// unmarshal-failure path (2xx but bad body) resp.Body is nil — the
		// nil check + len(b) > 0 guard handles that. On transport errors,
		// resp itself may be nil, but in that case the caller would have
		// panicked on resp.Status/StatusCode access above us, so we can
		// safely assume resp is non-nil here.
		//
		// Bounded read: cap at errorBodyCap via io.LimitReader to protect
		// against pathological origin responses.
		if resp.Body != nil {
			b, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyCap))
			if readErr == nil && len(b) > 0 {
				d.Body = b
			}
		}
		return nil, d, time.Duration(0), failedExts, faultStatus
	}

	var ts timeseries.Timeseries
	if len(mts) == 1 {
		ts = mts[0]
	} else if len(mts) > 1 {
		ts = mts[0]
		ts.Merge(true, mts[1:]...)
	}

	return ts, d, elapsed, failedExts, false
}

func recordDPCResult(
	r *http.Request,
	cacheStatus status.LookupStatus,
	httpStatus int,
	path, ffStatus string,
	elapsed float64,
	needed, failed timeseries.ExtentList, header http.Header, partials ...headers.PartialBucketResult,
) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus,
		elapsed, needed, failed, header, partials...)
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

func getTimeseriesReader(resp *http.Response) io.Reader {
	// a response that names its format, as ClickHouse's do, tells the unmarshaler how to read it
	reader := getDecoderReader(resp)
	if format := resp.Header.Get(hnClickHouseFormat); format != "" {
		return timeseries.NewFormatHintReader(reader, format)
	}
	return reader
}

func fetchConcurrencyLimit(o *bo.Options) int {
	if o != nil && o.FetchConcurrencyLimit > 0 {
		return o.FetchConcurrencyLimit
	}
	return bo.DefaultFetchConcurrencyLimit
}

// this will concurrently fetch provided requested extents
func fetchExtents(
	el timeseries.ExtentList,
	rsc *request.Resources,
	h http.Header,
	client backends.TimeseriesBackend,
	pr *proxyRequest,
	wur timeseries.UnmarshalerReaderFunc,
	span trace.Span,
	limiter fetchLimiter,
) (timeseries.List, int64, *http.Response, timeseries.ExtentList, bool) {
	var uncachedValueCount atomic.Int64
	var appendLock, respLock sync.Mutex

	// the list of time series created from the responses
	mts := make(timeseries.List, len(el))
	errTs := make(timeseries.ExtentList, len(el))
	// the meta-response aggregating all upstream responses
	mresp := &http.Response{Header: h}
	var errorHeaders http.Header

	// limit concurrent upstream requests to avoid overwhelming the origin
	eg := errgroup.Group{}
	eg.SetLimit(fetchConcurrencyLimit(rsc.BackendOptions))

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range el {
		// This concurrently fetches gaps from the origin and adds their datasets to the merge list
		eg.Go(func() error {
			// the request's partial bucket fetches share the limit
			limiter.acquire(nil)
			defer limiter.release()
			e, ok := gridFetchExtent(el[i], rsc)
			if !ok {
				return nil
			}
			rq := pr.Clone()
			mrsc := rsc.Clone()
			rq.upstreamRequest = rq.upstreamRequest.WithContext(tctx.WithResources(
				trace.ContextWithSpan(context.Background(), span),
				mrsc))
			rq.upstreamRequest = rq.upstreamRequest.WithContext(profile.ToContext(rq.upstreamRequest.Context(),
				dpcUpstreamEncodingProfile(mrsc.TSReqestOptions)))
			if err := client.SetExtent(rq.upstreamRequest, rsc.TimeRangeQuery, &e); err != nil {
				logger.Error("could not rewrite cache-miss time range query",
					logging.Pairs{keys.Error: err.Error(), keys.BackendName: client.Name()})
				errTs[i] = el[i]
				respLock.Lock()
				if mresp.StatusCode < http.StatusInternalServerError {
					mresp.Status = "500 Internal Server Error"
					mresp.StatusCode = http.StatusInternalServerError
				}
				respLock.Unlock()
				return nil
			}

			ctxMR, spanMR := tspan.NewChildSpan(rq.upstreamRequest.Context(), rsc.Tracer, "FetchRange")
			if spanMR != nil {
				rq.upstreamRequest = rq.upstreamRequest.WithContext(ctxMR)
				defer spanMR.End()
			}
			setResourceSpanAttributes(mrsc, spanMR)

			body, resp, _, fetchErr := rq.Fetch()
			if resp != nil {
				setHTTPStatusSpanAttributes(rsc.Tracer, resp.StatusCode, spanMR)
			}

			respLock.Lock()
			if resp.StatusCode > mresp.StatusCode {
				mresp.Status = resp.Status
				mresp.StatusCode = resp.StatusCode
			}
			respLock.Unlock()

			// Mid-stream read failure: 2xx + empty body would fall through
			// both branches below and nil-deref downstream in the merge.
			if fetchErr != nil {
				errTs[i] = el[i]
				return nil
			}

			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, ferr := wur(getTimeseriesReader(resp), rsc.TimeRangeQuery)
				if ferr != nil {
					logger.Error("proxy object unmarshaling failed",
						logging.Pairs{keys.Detail: ferr.Error()})
					errTs[i] = el[i]
					return nil
				}
				uncachedValueCount.Add(nts.ValueCount())
				nts.SetTimeRangeQuery(rsc.TimeRangeQuery)
				nts.SetExtents(timeseries.ExtentList{e})
				appendLock.Lock()
				headers.Merge(h, resp.Header)
				appendLock.Unlock()
				mts[i] = nts
			} else if resp.StatusCode != http.StatusOK {
				errTs[i] = el[i]
				var b []byte
				var s string
				if resp.Body != nil {
					var readErr error
					b, readErr = io.ReadAll(io.LimitReader(getDecoderReader(resp), errorBodyCap))
					if readErr != nil {
						logger.Warn("failed to read upstream error response body",
							logging.Pairs{keys.Detail: readErr.Error()})
					}
					s = string(b)
					respLock.Lock()
					if resp.StatusCode == mresp.StatusCode {
						mresp.Body = io.NopCloser(bytes.NewReader(b))
						errorHeaders = resp.Header.Clone()
						errorHeaders.Del(headers.NameContentLength)
					}
					respLock.Unlock()
				}
				if len(s) > 128 {
					s = s[:128]
				}
				logger.Error("unexpected upstream response",
					logging.Pairs{
						keys.StatusCode:           resp.StatusCode,
						"clientRequestURL":        pr.Request.URL.String(),
						"clientRequestMethod":     pr.Request.Method,
						"clientRequestHeaders":    headers.SanitizeForLogging(pr.Request.Header),
						"upstreamRequestURL":      pr.upstreamRequest.URL.String(),
						"upstreamRequestMethod":   pr.upstreamRequest.Method,
						"upstreamRequestHeaders":  headers.SanitizeForLogging(pr.upstreamRequest.Header),
						"upstreamResponseHeaders": headers.LogString(resp.Header),
						"upstreamResponseBody":    s,
					},
				)
			}
			return nil
		})
	}
	eg.Wait()

	fullFaults := false
	trimmedList := errTs.TrimEmptyExtents()
	if trimmedList.Len() == el.Len() {
		fullFaults = true
		if errorHeaders != nil {
			mresp.Header = errorHeaders
		}
	}

	return mts, uncachedValueCount.Load(), mresp, trimmedList, fullFaults
}
