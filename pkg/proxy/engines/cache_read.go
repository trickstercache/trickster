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
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"golang.org/x/sync/errgroup"
)

// ChunkQueryIterator provides iteration over chunks for cache queries (reading)
type ChunkQueryIterator interface {
	// IterateChunks calls the provided function for each chunk
	// The function receives (index, subkey) and should return whether to continue
	IterateChunks(func(int, string) bool)
}

// iterateChunksHelper is a generic helper that reduces code duplication for chunk iteration
func iterateChunksHelper(fn func(int, string) bool, generateSubkey func(int) string, shouldContinue func(int) bool) {
	for i := 0; shouldContinue(i); i++ {
		subkey := generateSubkey(i)
		if !fn(i, subkey) {
			break
		}
	}
}

// ChunkQueryProcessor handles the result of querying a single chunk from cache (reading)
type ChunkQueryProcessor interface {
	// ProcessChunk processes a successful query result for a chunk
	ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error

	// Finalize performs any final processing after all chunks are processed
	Finalize() error
}

// executeChunkQuery performs generic chunk querying with early cancellation (reading from cache)
func executeChunkQuery(ctx context.Context, c cache.Cache, iterator ChunkQueryIterator, processor ChunkQueryProcessor, opts *options.Options) error {
	// Prepare waitgroup for concurrent processing
	eg := errgroup.Group{}
	limit := options.DefaultChunkReadConcurrencyLimit
	if opts != nil && opts.ChunkReadConcurrencyLimit != 0 {
		limit = opts.ChunkReadConcurrencyLimit
	}
	eg.SetLimit(limit)

	// Early cancellation context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr atomic.Pointer[error]

	iterator.IterateChunks(func(index int, subkey string) bool {
		// Check if we should abort due to previous error
		select {
		case <-ctx.Done():
			return false
		default:
		}

		eg.Go(func() error {
			qr := queryConcurrent(ctx, c, subkey)
			if qr.lookupStatus != status.LookupStatusHit &&
				qr.lookupStatus != status.LookupStatusProxyHit &&
				(qr.err == nil || errors.Is(qr.err, cache.ErrKNF)) {
				if mr, ok := processor.(chunkMissRecorder); ok {
					mr.ChunkMissed(index)
				}
				return nil
			}
			if qr.err != nil {
				// Set first error and cancel remaining operations
				if firstErr.CompareAndSwap(nil, &qr.err) {
					cancel()
				}
				logger.Error("chunk query failed",
					logging.Pairs{
						keys.Error:         qr.err,
						"chunkIdx":         index,
						keys.Key:           subkey,
						"cacheQueryStatus": qr.lookupStatus,
					})
				return qr.err
			}

			// Process the successful result
			if err := processor.ProcessChunk(index, subkey, qr, c); err != nil {
				if firstErr.CompareAndSwap(nil, &err) {
					cancel()
				}
				return err
			}
			return nil
		})
		return true
	})

	// Wait on queries
	if err := eg.Wait(); err != nil {
		return err
	}

	// Check if we had any errors
	if err := firstErr.Load(); err != nil {
		return *err
	}

	// Finalize processing
	return processor.Finalize()
}

// chunkMissRecorder is a ChunkQueryProcessor that must learn which chunks the cache didn't hold
type chunkMissRecorder interface {
	ChunkMissed(index int)
}

// TimeseriesChunkQueryIterator implements ChunkQueryIterator for timeseries chunks (reading)
type TimeseriesChunkQueryIterator struct {
	key   string
	cext  timeseries.Extent
	csize time.Duration
	trq   *timeseries.TimeRangeQuery
}

func (tci *TimeseriesChunkQueryIterator) IterateChunks(fn func(int, string) bool) {
	chunkStart := tci.cext.Start

	iterateChunksHelper(fn,
		func(i int) string {
			chunkExtent := timeseries.Extent{
				Start: chunkStart.Add(time.Duration(i) * tci.csize),
				End:   chunkStart.Add(time.Duration(i) * tci.csize).Add(tci.csize - tci.trq.Step),
			}
			return getSubKey(tci.key, chunkExtent)
		},
		func(i int) bool {
			return chunkStart.Add(time.Duration(i) * tci.csize).Before(tci.cext.End)
		})
}

// TimeseriesChunkQueryProcessor implements ChunkQueryProcessor for timeseries chunks (reading)
type TimeseriesChunkQueryProcessor struct {
	d         *HTTPDocument
	trq       *timeseries.TimeRangeQuery
	unmarshal timeseries.UnmarshalerFunc
	ress      timeseries.List
}

func (tcp *TimeseriesChunkQueryProcessor) ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error {
	if c.Configuration().Provider != providerMemory {
		var err error
		qr.d.timeseries, err = tcp.unmarshal(qr.d.Body, nil)
		if err != nil {
			logger.Error("chunk unmarshal failed",
				logging.Pairs{
					keys.Error:         err,
					"chunkIdx":         index,
					keys.Key:           subkey,
					"cacheQueryStatus": qr.lookupStatus,
				})
			return err
		}
	}
	if qr.d.timeseries != nil {
		ts := qr.d.timeseries
		if c.Configuration().Provider == providerMemory {
			// a memory cache's chunk is read in place, and the merge of the chunks takes over their
			// series, so it is given views of them, whose series are its own
			ts = responseView(ts, timeseries.Extent{}, false)
		}
		tcp.ress[index] = ts
	}
	return nil
}

func (tcp *TimeseriesChunkQueryProcessor) Finalize() error {
	// every chunk is this read's own, a view or a decoding, so the first needs no copy to merge into
	tcp.d.timeseries = tcp.ress.Merge(false)
	if tcp.d.timeseries != nil {
		tcp.d.timeseries.SetExtents(tcp.d.timeseries.Extents().Compress(tcp.trq.Step))
	}
	return nil
}

// executeTimeseriesChunkQuery performs timeseries chunk querying with early cancellation
func executeTimeseriesChunkQuery(ctx context.Context, c cache.Cache, key string, d *HTTPDocument, trq *timeseries.TimeRangeQuery, unmarshal timeseries.UnmarshalerFunc, opts *options.Options) error {
	// Determine chunk extent and number of chunks
	csize := trq.Step * time.Duration(c.Configuration().TimeseriesChunkFactor)
	cext := timeseriesChunkExtent(trq, csize)
	cct := int(cext.End.Sub(cext.Start) / csize)

	iterator := &TimeseriesChunkQueryIterator{
		key:   key,
		cext:  cext,
		csize: csize,
		trq:   trq,
	}

	processor := &TimeseriesChunkQueryProcessor{
		d:         d,
		trq:       trq,
		unmarshal: unmarshal,
		ress:      make(timeseries.List, cct),
	}

	return executeChunkQuery(ctx, c, iterator, processor, opts)
}

// ByterangeChunkQueryIterator implements ChunkQueryIterator for byterange chunks (reading)
type ByterangeChunkQueryIterator struct {
	key  string
	crs  int64
	cre  int64
	size int64
}

func (bci *ByterangeChunkQueryIterator) IterateChunks(fn func(int, string) bool) {
	iterateChunksHelper(fn,
		func(i int) string {
			chunkStart := bci.crs + int64(i)*bci.size
			chunkRange := byterange.Range{
				Start: chunkStart,
				End:   chunkStart + bci.size - 1,
			}
			return bci.key + chunkRange.String()
		},
		func(i int) bool {
			return bci.crs+int64(i)*bci.size < bci.cre
		})
}

// ByterangeChunkQueryProcessor implements ChunkQueryProcessor for byterange chunks (reading)
type ByterangeChunkQueryProcessor struct {
	d    *HTTPDocument
	size int64
	crs  int64  // start of the first chunk read
	cre  int64  // end of the last chunk read, exclusive
	dbl  *int64 // atomic counter for document body length

	mu      sync.Mutex
	missing byterange.Ranges // windows of chunks that were not in the cache
}

func (bcp *ByterangeChunkQueryProcessor) ProcessChunk(index int, subkey string, qr *queryResult, c cache.Cache) error {
	if qr == nil {
		return nil
	}

	// Handle error - different from timeseries as we allow cache.ErrKNF
	if qr.err != nil && !errors.Is(qr.err, cache.ErrKNF) {
		return qr.err
	}

	if qr.d == nil || qr.lookupStatus != status.LookupStatusHit {
		bcp.ChunkMissed(index)
		return nil
	}
	if qr.d.IsMeta {
		return nil
	}
	for _, r := range qr.d.Ranges {
		content := qr.d.Body[r.Start%bcp.size : r.End%bcp.size+1]
		r.Copy(bcp.d.Body, content)
		// chunks finish concurrently, so a lost swap is retried until the length covers this range
		for end := r.End + 1; ; {
			v := atomic.LoadInt64(bcp.dbl)
			if end <= v || atomic.CompareAndSwapInt64(bcp.dbl, v, end) {
				break
			}
		}
	}
	return nil
}

// ChunkMissed notes a chunk the cache didn't hold; an evicted or expired chunk's bytes can't be served
func (bcp *ByterangeChunkQueryProcessor) ChunkMissed(index int) {
	start := bcp.crs + int64(index)*bcp.size
	bcp.mu.Lock()
	bcp.missing = append(bcp.missing, byterange.Range{Start: start, End: start + bcp.size - 1})
	bcp.mu.Unlock()
}

func (bcp *ByterangeChunkQueryProcessor) Finalize() error {
	if len(bcp.missing) > 0 {
		rs := bcp.d.Ranges
		if len(rs) == 0 && len(bcp.d.unloadedRanges) == 0 {
			// a whole object missing a chunk is reduced to the parts that were read and found
			rs, bcp.d.unloadedRanges = splitRangesByWindow(byterange.Ranges{{Start: 0, End: bcp.d.ContentLength - 1}},
				byterange.Range{Start: bcp.crs, End: bcp.cre - 1})
		}
		for _, m := range bcp.missing {
			_, rs = splitRangesByWindow(rs, m)
		}
		if len(rs) == 0 && len(bcp.d.unloadedRanges) == 0 {
			return cache.ErrKNF
		}
		bcp.d.Ranges = rs
	}
	if len(bcp.d.Ranges) > 1 {
		bcp.d.StoredRangeParts = make(map[string]*byterange.MultipartByteRange)
		for _, r := range bcp.d.Ranges {
			bcp.d.StoredRangeParts[r.String()] = &byterange.MultipartByteRange{
				Range:   r,
				Content: bcp.d.Body[r.Start : r.End+1],
			}
		}
		bcp.d.Body = nil
	} else {
		bcp.d.Body = bcp.d.Body[:*bcp.dbl]
	}
	return nil
}

// executeByterangeChunkQuery performs byterange chunk querying with early cancellation
func executeByterangeChunkQuery(ctx context.Context, c cache.Cache, key string, d *HTTPDocument, ranges byterange.Ranges, opts *options.Options) error {
	// Determine chunk start/end
	var crs, cre int64
	if len(ranges) == 0 {
		ranges = byterange.Ranges{byterange.Range{Start: 0, End: d.ContentLength - 1}}
	}
	size := c.Configuration().ByterangeChunkSize
	crs, cre = ranges[0].Start, ranges[len(ranges)-1].End
	crs = (crs / size) * size
	cre = (cre/size + 1) * size

	// only the chunks under the requested ranges are read, so the document claims just the bytes they hold
	if len(d.Ranges) > 0 {
		d.Ranges, d.unloadedRanges = splitRangesByWindow(d.Ranges, byterange.Range{Start: crs, End: cre - 1})
	}

	// Allocate body in meta document
	d.Body = make([]byte, d.ContentLength)

	var dbl int64 // Track document body length

	iterator := &ByterangeChunkQueryIterator{
		key:  key,
		crs:  crs,
		cre:  cre,
		size: size,
	}

	processor := &ByterangeChunkQueryProcessor{
		d:    d,
		size: size,
		crs:  crs,
		cre:  cre,
		dbl:  &dbl,
	}

	return executeChunkQuery(ctx, c, iterator, processor, opts)
}

// splitRangesByWindow returns the parts of rs inside w and the parts outside it
func splitRangesByWindow(rs byterange.Ranges, w byterange.Range) (in, out byterange.Ranges) {
	if !slices.ContainsFunc(rs, func(r byterange.Range) bool { return r.Start < w.Start || r.End > w.End }) {
		return rs, nil
	}
	for _, r := range rs {
		if r.End < w.Start || r.Start > w.End {
			out = append(out, r)
			continue
		}
		if r.Start < w.Start {
			out = append(out, byterange.Range{Start: r.Start, End: w.Start - 1})
		}
		if r.End > w.End {
			out = append(out, byterange.Range{Start: w.End + 1, End: r.End})
		}
		in = append(in, byterange.Range{Start: max(r.Start, w.Start), End: min(r.End, w.End)})
	}
	return in, out
}
