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
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	tc "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

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
	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "DeltaProxyCacheRequest")
	if span != nil {
		defer span.End()
	}
	r = r.WithContext(ctx)

	pc := rsc.PathConfig
	cache := rsc.CacheClient
	cc := rsc.CacheConfig
	locker := cache.Locker()

	client := rsc.BackendClient.(backends.TimeseriesBackend)

	trq, rlo, canOPC, err := client.ParseTimeRangeQuery(r)
	rsc.TimeRangeQuery = trq
	rsc.TSReqestOptions = rlo
	if err != nil {
		if canOPC {
			tl.Debug(rsc.Logger, "could not parse time range query, using object proxy cache", tl.Pairs{"error": err.Error()})
			rsc.AlternateCacheTTL = time.Second * o.FastForwardTTL
			ObjectProxyCacheRequest(w, r)
			return
		}
		// err may simply mean incompatible query (e.g., non-select), so just proxy
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
			tl.Debug(pr.Logger, "timerange end is too old to consider caching",
				tl.Pairs{"oldestRetainedTimestamp": OldestRetainedTimestamp,
					"step": trq.Step, "retention": o.TimeseriesRetention})
			DoProxy(w, r, true)
			return
		}
	}

	client.SetExtent(pr.upstreamRequest, trq, &trq.Extent)
	key := o.CacheKeyPrefix + ".dpc." + pr.DeriveCacheKey("")
	pr.cacheLock, _ = locker.RAcquire(key)

	// this is used to determine if Fast Forward should be activated for this request
	normalizedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: now},
		Step:   trq.Step,
	}
	normalizedNow.NormalizeExtent()

	var cts timeseries.Timeseries
	var doc *HTTPDocument
	var elapsed time.Duration

	coReq := GetRequestCachingPolicy(r.Header)
checkCache:
	if coReq.NoCache {
		if span != nil {
			span.AddEvent("Not Caching")
		}
		cacheStatus = status.LookupStatusPurge
		go cache.Remove(key)
		cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
		if err != nil {
			pr.cacheLock.RRelease()
			h := doc.SafeHeaderClone()
			recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
				r.URL.Path, "", elapsed.Seconds(), nil, h)
			Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
			return // fetchTimeseries logs the error
		}
	} else {
		doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil, modeler.CacheUnmarshaler)
		if cacheStatus == status.LookupStatusKeyMiss && err == tc.ErrKNF {
			cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
			if err != nil {
				pr.cacheLock.RRelease()
				h := doc.SafeHeaderClone()
				recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
					r.URL.Path, "", elapsed.Seconds(), nil, h)
				Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
				return // fetchTimeseries logs the error
			}
		} else {
			// Load the Cached Timeseries
			if doc == nil {
				err = tpe.ErrEmptyDocumentBody
			} else {
				cts = doc.timeseries
			}
			if err != nil {
				tl.Error(pr.Logger, "cache object unmarshaling failed",
					tl.Pairs{"key": key, "backendName": client.Name(), "detail": err.Error()})
				go cache.Remove(key)
				cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
				if err != nil {
					pr.cacheLock.RRelease()
					h := doc.SafeHeaderClone()
					recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
						r.URL.Path, "", elapsed.Seconds(), nil, h)
					Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
					return // fetchTimeseries logs the error
				}
			} else {
				if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodLRU {
					el := cts.Extents()
					tsc := cts.TimestampCount()
					if tsc > 0 &&
						tsc >= int64(o.TimeseriesRetentionFactor) {
						if trq.Extent.End.Before(el[0].Start) {
							pr.cacheLock.RRelease()
							tl.Debug(pr.Logger, "timerange end is too old to consider caching",
								tl.Pairs{"step": trq.Step, "retention": o.TimeseriesRetention})
							DoProxy(w, r, true)
							return
						}
					}
				}
				cacheStatus = status.LookupStatusPartialHit
			}
		}
	}

	// Find the ranges that we want, but which are not currently cached
	var missRanges, vr, cvr timeseries.ExtentList
	if cts != nil {
		vr = cts.VolatileExtents()
	}
	if cacheStatus == status.LookupStatusPartialHit {
		missRanges = cts.Extents().CalculateDeltas(trq.Extent, trq.Step)
		// this is the backfill part of backfill tolerance. if there are any volatile
		// ranges in the timeseries, this determines if any fall within the client's
		// requested range and ensures they are re-requested. this only happens if
		// the request is already a phit
		if bt > 0 && len(missRanges) > 0 && len(vr) > 0 {
			// this checks the timeseries's volatile ranges for any overlap with
			// the request extent, and adds those to the missRanges to refresh
			if cvr = vr.Crop(trq.Extent); len(cvr) > 0 {
				missRanges = append(missRanges, cvr...).Compress(trq.Step)
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

	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", cacheStatus.String()))

	var writeLock locks.NamedLock

	if cacheStatus == status.LookupStatusHit {
		// In a cache hit, nothing changes so we just release the reader lock
		pr.cacheLock.RRelease()
	} else {
		// in this case, it's not a cache hit, so something is _likely_ going to be cached now.
		// we write lock here, so as to prevent other concurrent client requests for the same url,
		// which will have the same cacheStatus, from causing the same or similar HTTP requests
		// to be made against the origin, since just one should do.

		// acquire a write lock via the Upgrade method, which will swap the read lock for a
		// write lock, and return true if this client was the only one, or otherwise the first
		// client in a concurrent read lock group to request an Upgrade.
		wasFirst := pr.cacheLock.Upgrade()

		// if this request was first, it is good to proceed with upstream communications and caching.
		// when another requests was first to acquire the mutex, we will jump up to checkCache
		// to get the refreshed version. after 3 reiterations, we'll proceed anyway to avoid long loops.
		if !wasFirst && pr.rerunCount < 3 {
			// we weren't first, so quickly drop our write lock, and re-run the request
			pr.cacheLock.Release()
			pr.cacheLock, _ = locker.RAcquire(key)
			pr.rerunCount++
			goto checkCache
		}
		writeLock = pr.cacheLock
	}

	ffStatus := "off"
	var ffReq *http.Request
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	if !rlo.FastForwardDisable {
		if trq.Step > o.FastForwardTTL {
			ffReq, err = client.FastForwardRequest(r)
			if err != nil || ffReq == nil || ffReq.URL == nil || ffReq.URL.Scheme == "" {
				ffStatus = "err"
				rlo.FastForwardDisable = true
			} else {
				ffReq = ffReq.WithContext(profile.ToContext(ffReq.Context(), dpcEncodingProfile.Clone()))
				rs := request.NewResources(o, o.FastForwardPath, cc, cache, client, rsc.Tracer, pr.Logger)
				rs.AlternateCacheTTL = o.FastForwardTTL
				ffReq = ffReq.WithContext(tctx.WithResources(ffReq.Context(), rs))
			}
		} else {
			rlo.FastForwardDisable = true
		}
	}

	// maintain a list of timeseries to merge into the main timeseries
	wg := &sync.WaitGroup{}

	var hasFastForwardData bool
	var ffts timeseries.Timeseries

	// Only fast forward if configured and the user request is for the absolute latest datapoint
	if (!rlo.FastForwardDisable) &&
		(trq.Extent.End.Equal(normalizedNow.Extent.End)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, span := tspan.NewChildSpan(ctx, rsc.Tracer, "FetchFastForward")
			if span != nil {
				ffReq = ffReq.WithContext(trace.ContextWithSpan(ffReq.Context(), span))
				defer span.End()
			}
			body, resp, isHit := FetchViaObjectProxyCache(ffReq)
			if resp != nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
				ffts, err = modeler.WireUnmarshalerReader(getDecoderReader(resp), trq)
				if err != nil {
					ffStatus = "err"
					tl.Error(pr.Logger, "proxy object unmarshaling failed",
						tl.Pairs{"body": string(body)})
					return
				}
				ffts.SetTimeRangeQuery(trq)
				x := ffts.Extents()
				if isHit {
					ffStatus = "hit"
				} else {
					ffStatus = "miss"
				}
				hasFastForwardData = len(x) > 0 && x[0].End.After(trq.Extent.End)
			} else {
				ffStatus = "err"
			}
		}()
	}

	// while fast forward fetching is occurring in a goroutine, this section will
	// fetch any uncached ranges in cases of partial hit or range miss
	dpStatus := tl.Pairs{
		"cacheKey":    key,
		"cacheStatus": cacheStatus,
		"reqStart":    trq.Extent.Start.Unix(),
		"reqEnd":      trq.Extent.End.Unix(),
	}

	var mts []timeseries.Timeseries
	var uncachedValueCount int64
	var mresp *http.Response

	var ferr error

	// this concurrently fetches all missing ranges from the origin
	if len(missRanges) > 0 {
		if o.DoesShard {
			missRanges = missRanges.Splice(trq.Step, o.MaxShardSize, o.ShardStep, o.MaxShardSizePoints)
		}
		dpStatus["extentsFetched"] = missRanges.String()
		frsc := request.NewResources(o, pc, cc, cache, client, rsc.Tracer, pr.Logger)
		frsc.TimeRangeQuery = trq
		mts, uncachedValueCount, mresp, ferr = fetchExtents(missRanges, frsc, doc.Headers, client,
			pr, modeler.WireUnmarshalerReader, span)
	}

	wg.Wait()

	if ferr != nil {
		if writeLock != nil {
			writeLock.Release()
		}
		Respond(w, mresp.StatusCode, mresp.Header, mresp.Body)
		return
	}

	// Merge the new delta timeseries into the cached timeseries
	if len(mts) > 0 {
		// on phit, elapsed records the time spent waiting for all upstream requests to complete
		elapsed = time.Since(now)
		cts.Merge(true, mts...)
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

	if writeLock != nil {
		// if the mutex is still locked, it means we need to write the time series to cache
		go func() {
			defer writeLock.Release()
			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the
			// Backfill Tolerance before storing to cache
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
				if err := WriteCache(ctx, cache, key, doc, o.TimeseriesTTL, o.CompressibleTypes, modeler.CacheMarshaler); err != nil {
					tl.Error(pr.Logger, "error writing object to cache",
						tl.Pairs{
							"backendName": o.Name,
							"cacheName":   cache.Configuration().Name,
							"cacheKey":    key,
							"detail":      err.Error(),
						},
					)
				}
			}
		}()
	}

	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}

	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "cached", r.URL.Path).Add(float64(cachedValueCount))
	}

	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was normalized to stepboundaries and would remove fast forward data
	// If the fast forward data point is older (e.g. cached) than the last datapoint in the
	// returned time series, it will not be merged
	if hasFastForwardData && len(ffts.Extents()) == 1 &&
		ffts.Extents()[0].Start.Truncate(time.Second).After(normalizedNow.Extent.End) {
		rts.Merge(false, ffts)
	}
	rts.SetExtents(nil) // so they are not included in the client response json
	//rts.SetTimeRangeQuery(&timeseries.TimeRangeQuery{})
	rh := doc.SafeHeaderClone()
	sc := doc.StatusCode

	// Respond to the user. Using the response headers from a Delta Response,
	// so as to not map conflict with cacheData on WriteCache
	logDeltaRoutine(pr.Logger, dpStatus)
	recordDPCResult(r, cacheStatus, sc, r.URL.Path, ffStatus, elapsed.Seconds(), missRanges, rh)

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
	modeler.WireMarshalWriter(rts, rlo, sc, w)
}

func logDeltaRoutine(logger interface{}, p tl.Pairs) {
	tl.Debug(logger, "delta routine completed", p)
}

var dpcEncodingProfile = &profile.Profile{
	ClientAcceptEncoding: providers.AllSupportedWebProviders,
	Supported:            7,
	SupportedHeaderVal:   providers.AllSupportedWebProviders,
}

func fetchTimeseries(pr *proxyRequest, trq *timeseries.TimeRangeQuery,
	client backends.TimeseriesBackend, modeler *timeseries.Modeler) (timeseries.Timeseries,
	*HTTPDocument, time.Duration, error) {

	rsc := request.GetResources(pr.Request).Clone()
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
		o.MaxShardSize, o.ShardStep, o.MaxShardSizePoints), rsc,
		http.Header{}, client, pr, modeler.WireUnmarshalerReader, nil)

	// elaspsed measures only the time spent making origin requests
	var elapsed time.Duration
	if err == nil {
		elapsed = time.Since(start)
	}

	go logUpstreamRequest(pr.Logger, o.Name, o.Provider, handlerName,
		pr.Method, pr.URL.String(), pr.UserAgent(), resp.StatusCode, 0, elapsed.Seconds())

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}

	if err != nil {
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

func recordDPCResult(r *http.Request, cacheStatus status.LookupStatus, httpStatus int, path,
	ffStatus string, elapsed float64, needed []timeseries.Extent, header http.Header) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus, elapsed,
		timeseries.ExtentList(needed), header)
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
	span trace.Span) ([]timeseries.Timeseries, int64, *http.Response, error) {

	var uncachedValueCount atomic.Int64
	var wg sync.WaitGroup
	var appendLock, respLock sync.Mutex
	var err error

	// the list of time series created from the responses
	mts := make([]timeseries.Timeseries, 0, len(el))
	// the meta-response aggregating all upstream responses
	mresp := &http.Response{Header: h}

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range el {
		wg.Add(1)
		// This concurrently fetches gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, rq *proxyRequest) {
			defer wg.Done()
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
					tl.Error(pr.Logger, "proxy object unmarshaling failed",
						tl.Pairs{"detail": ferr.Error()})
					appendLock.Lock()
					if err == nil {
						err = ferr
					}
					appendLock.Unlock()
					return
				}
				uncachedValueCount.Add(nts.ValueCount())
				nts.SetTimeRangeQuery(rsc.TimeRangeQuery)
				nts.SetExtents([]timeseries.Extent{*e})
				appendLock.Lock()
				headers.Merge(h, resp.Header)
				mts = append(mts, nts)
				appendLock.Unlock()
			} else if resp.StatusCode != 200 {
				err = tpe.ErrUnexpectedUpstreamResponse
				var b []byte
				var s string
				if resp.Body != nil {
					b, _ = io.ReadAll(resp.Body)
					s = string(b)
					respLock.Lock()
					mresp.Body = io.NopCloser(bytes.NewReader(b))
					respLock.Unlock()
				}
				if len(s) > 128 {
					s = s[:128]
				}
				tl.Error(pr.Logger, "unexpected upstream response",
					tl.Pairs{
						"statusCode":              resp.StatusCode,
						"clientRequestURL":        pr.Request.URL.String(),
						"clientRequestMethod":     pr.Request.Method,
						"clientRequestHeaders":    pr.Request.Header,
						"upstreamRequestURL":      pr.upstreamRequest.URL.String(),
						"upstreamRequestMethod":   pr.upstreamRequest.Method,
						"upstreamRequestHeaders":  headers.LogString(pr.upstreamRequest.Header),
						"upstreamResponseHeaders": headers.LogString(resp.Header),
						"upstreamResponseBody":    s,
					},
				)
			}
		}(&el[i], pr.Clone())
	}
	wg.Wait()
	return mts, uncachedValueCount.Load(), mresp, err
}
