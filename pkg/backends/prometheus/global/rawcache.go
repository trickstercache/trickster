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

package global

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
)

const (
	// rawKeyPrefix starts the key of every cached block of raw series
	rawKeyPrefix = "tsm-global.trickstercache.org.raw."
	// rawCacheVersion is part of every key, so a change to what blocks hold is a miss
	rawCacheVersion = "1"
	// rawBlockMs is the span of each cached block, aligned to the epoch as TSDB blocks are
	rawBlockMs = int64(2 * time.Hour / time.Millisecond)
	// rawCacheProtocol labels the cache engine's logs and failure metrics
	rawCacheProtocol = "tsm_global_raw"
	rawLockStripes   = 64
	// rawGapReads bounds how many of one read's missing extents are read from the member at once
	rawGapReads = 4
	// memberPanicEvent is logged when reading or decoding a member's answer panics
	memberPanicEvent = "tsm global evaluation member read panicked"
)

// RawCacheOptions configures a RawCache.
type RawCacheOptions struct {
	// Name is the ALB's name, for logs
	Name string
	// TTL is how long a block is kept after it is last stored
	TTL time.Duration
	// VolatileWindow, when set, is how far back from now every member's samples are always read and never
	// kept; when nil, each source's own VolatileWindow is
	VolatileWindow *time.Duration
	// Window is how far back from now samples are kept, by whole blocks; older ones are always read from
	// members. Zero keeps samples of any age
	Window time.Duration
	// MaxSeries, MaxSamples and MaxResponseBytes bound each member read, as an evaluation's Budget does
	MaxSeries, MaxSamples, MaxResponseBytes int64
	// Observe, when set, learns the cache status of each member read: hit, phit, kmiss or proxy-only
	Observe func(status cachestatus.LookupStatus)
	// Subsumed, when set, learns of each member read answered in part from a broader selector's blocks
	Subsumed func()
}

// RawCache keeps members' raw series in a cache, in blocks of time keyed by member, request identity and
// matchers, so a member is read only for what it lacks and the volatile window.
type RawCache struct {
	opts    RawCacheOptions
	engine  *nativedelta.Engine[*rawBlock]
	flights flightGroup[fetched]
	// locks serialize storing each block, by its key's stripe, so no store loses another's samples
	locks [rawLockStripes]sync.Mutex
	// index remembers which selectors' blocks are cached, so a narrower selector can read a broader one's
	index rawIndex
	now   func() time.Time
}

// NewRawCache returns a RawCache keeping blocks in c.
func NewRawCache(c cache.Cache, opts RawCacheOptions) *RawCache {
	rc := &RawCache{opts: opts, now: time.Now}
	rc.engine = nativedelta.New[*rawBlock](nativedelta.Config{
		Protocol:    rawCacheProtocol,
		BackendName: opts.Name,
		CacheClient: func() cache.Cache { return c },
		CacheTTL:    opts.TTL,
	}, rawBlockCodec{})
	return rc
}

// Wait returns once every member read started so far has stored what it keeps.
func (c *RawCache) Wait() { c.flights.running.Wait() }

// fetched is a member read's series, normalized to the span it was read for
type fetched struct {
	series   []rawSeries
	warnings annotations.Annotations
	err      error
}

// Select reads m's series matching every matcher from mint to maxt, each limited to those samples, reading
// m only for what the cache lacks and doesn't keep: the volatile window, and samples older than its window.
func (c *RawCache) Select(ctx context.Context, m Member, mint, maxt int64, hints *storage.SelectHints,
	matchers []*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	k, ok := c.key(ctx, m.Source, matchers)
	now := c.now()
	keep := c.keeps(now, m.Source)
	if !ok || mint > maxt || maxt < keep.mint {
		c.observe(cachestatus.LookupStatusProxyOnly)
		series, warnings, err := m.Source.Select(ctx, mint, maxt, hints, matchers...)
		if err != nil {
			return nil, nil, err
		}
		return bound(series, mint, maxt), warnings, nil
	}
	sources, covered, subsumed := c.cached(k, max(mint, keep.mint), min(maxt, keep.maxt), now)
	if subsumed && c.opts.Subsumed != nil {
		c.opts.Subsumed()
	}
	missing := covered.CalculateDeltas(timeseries.ExtentList{extent(mint, maxt)}, time.Millisecond)
	results, err := c.fetchAll(ctx, k, m, missing, keep, hints, matchers)
	if err != nil {
		return nil, nil, err
	}
	var warnings annotations.Annotations
	for i, r := range results {
		warnings.Merge(r.warnings)
		sources = append(sources, rawSource{
			series: r.series,
			lo:     missing[i].Start.UnixMilli(), hi: missing[i].End.UnixMilli(),
		})
	}
	c.observe(readStatus(covered, missing, mint, keep.maxt))
	series, cachedSamples, bad, err := assemble(sources)
	if err != nil {
		// a cached block whose samples can't be read is dropped, so the next read fetches them again
		c.engine.Store(bad, &nativedelta.Entry[*rawBlock]{Marker: true})
		return nil, nil, err
	}
	// the samples read from the member were counted as they arrived; the series and cached samples are now
	if err := budgetFrom(ctx).add(int64(len(series)), cachedSamples); err != nil {
		return nil, nil, err
	}
	return series, warnings, nil
}

// fetchAll reads the gaps from m, a few at a time; the first failure, as the evaluation running out of
// budget, cancels the reads not yet done
func (c *RawCache) fetchAll(ctx context.Context, k rawKey, m Member, gaps timeseries.ExtentList,
	keep span, hints *storage.SelectHints, matchers []*labels.Matcher,
) ([]fetched, error) {
	results := make([]fetched, len(gaps))
	if len(gaps) == 1 {
		results[0] = c.fetch(ctx, k, m, gaps[0], keep, hints, matchers)
		return results, results[0].err
	}
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(gaps), rawGapReads) {
		wg.Go(func() {
			for i := int(next.Add(1)) - 1; i < len(gaps) && gctx.Err() == nil; i = int(next.Add(1)) - 1 {
				if results[i] = c.fetch(gctx, k, m, gaps[i], keep, hints, matchers); results[i].err != nil {
					cancel()
					return
				}
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var errs []error
	for _, r := range results {
		// a read this select canceled after another failed reports only that failure
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			errs = append(errs, r.err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if gctx.Err() != nil {
		return nil, gctx.Err()
	}
	return results, nil
}

// keeps returns the span of src's samples the cache keeps at now: from the oldest block within its window,
// never before the epoch, to where the volatile window begins
func (c *RawCache) keeps(now time.Time, src Source) span {
	volatile := src.VolatileWindow()
	if c.opts.VolatileWindow != nil {
		volatile = *c.opts.VolatileWindow
	}
	keep := span{maxt: now.Add(-volatile).UnixMilli()}
	if c.opts.Window > 0 {
		keep.mint = max(blockIndex(now.Add(-c.opts.Window).UnixMilli())*rawBlockMs, 0)
	}
	return keep
}

// readStatus returns hit when a read took only volatile samples from the member, phit when it took older
// ones too, kmiss when none were cached, proxy-only when all were volatile
func readStatus(covered, missing timeseries.ExtentList, mint, cutoffMs int64) cachestatus.LookupStatus {
	switch {
	case mint > cutoffMs:
		return cachestatus.LookupStatusProxyOnly
	case len(covered) == 0:
		return cachestatus.LookupStatusKeyMiss
	case len(missing) > 0 && missing[0].Start.UnixMilli() <= cutoffMs:
		return cachestatus.LookupStatusPartialHit
	}
	return cachestatus.LookupStatusHit
}

func (c *RawCache) observe(status cachestatus.LookupStatus) {
	if c.opts.Observe != nil {
		c.opts.Observe(status)
	}
}

// cached returns the cached blocks holding samples of k from mint to maxt, with the extents they cover. What
// k's own blocks lack is taken from broader selectors' blocks when they hold it; subsumed reports whether any did.
func (c *RawCache) cached(k rawKey, mint, maxt int64, now time.Time,
) (sources []rawSource, covered timeseries.ExtentList, subsumed bool) {
	if mint > maxt {
		return nil, nil, false
	}
	var candidates []rawCandidate
	looked, own := false, false
	for b := blockIndex(mint); b <= blockIndex(maxt); b++ {
		lo, hi := max(mint, b*rawBlockMs), min(maxt, (b+1)*rawBlockMs-1)
		have := c.cachedBlock(&sources, k.key, b, lo, hi, nil, nil)
		own = own || len(have) > 0
		for i := 0; ; i++ {
			need := have.CalculateDeltas(timeseries.ExtentList{extent(lo, hi)}, time.Millisecond)
			if len(need) == 0 {
				break
			}
			if !looked {
				candidates, looked = c.index.candidates(k, now), true
			}
			if i == len(candidates) {
				break
			}
			if got := c.cachedBlock(&sources, candidates[i].key, b, lo, hi, need, candidates[i].extra); len(got) > 0 {
				have, subsumed = append(have, got...), true
			}
		}
		covered = append(covered, have...)
	}
	if own {
		c.index.record(k, now, c.opts.TTL)
	}
	return sources, covered, subsumed
}

// cachedBlock adds the source of block b of key from lo to hi to sources, returning the extents it covers.
// A broader selector's block adds only what need names, each part its own source, so no sample is read twice.
func (c *RawCache) cachedBlock(sources *[]rawSource, key string, b, lo, hi int64, need timeseries.ExtentList,
	match []*labels.Matcher,
) timeseries.ExtentList {
	bk := blockKey(key, b)
	entry, ok := c.engine.Retrieve(bk)
	if !ok || entry.Marker || entry.Payload == nil {
		return nil
	}
	// what a block holds past hi, as after the volatile window has grown, is read again
	e := entry.Extents.Crop(extent(lo, hi))
	if need == nil {
		if len(e) > 0 {
			*sources = append(*sources, rawSource{series: entry.Payload.series, lo: lo, hi: hi, block: bk})
		}
		return e
	}
	var got timeseries.ExtentList
	for _, n := range need {
		for _, part := range e.Crop(n) {
			got = append(got, part)
			*sources = append(*sources, rawSource{
				series: entry.Payload.series, lo: part.Start.UnixMilli(),
				hi: part.End.UnixMilli(), block: bk, match: match,
			})
		}
	}
	return got
}

// fetch reads m's series within gap, sharing the read with concurrent callers asking for the same, and
// keeps what lies within keep once the member answered completely
func (c *RawCache) fetch(ctx context.Context, k rawKey, m Member, gap timeseries.Extent, keep span,
	hints *storage.SelectHints, matchers []*labels.Matcher,
) fetched {
	mint, maxt := gap.Start.UnixMilli(), gap.End.UnixMilli()
	flight := k.key + "/" + strconv.FormatInt(mint, 10) + "/" + strconv.FormatInt(maxt, 10)
	r, err := c.flights.do(ctx, flight, budgetFrom(ctx), func(ctx context.Context, share func(int64) error) fetched {
		return c.read(ctx, m, mint, maxt, hints, matchers, share)
	}, func(r fetched) {
		if lo, hi := max(mint, keep.mint), min(maxt, keep.maxt); r.err == nil && len(r.warnings) == 0 && lo <= hi &&
			c.store(k.key, r.series, lo, hi) {
			c.index.record(k, c.now(), c.opts.TTL)
		}
	})
	if err != nil {
		return fetched{err: err}
	}
	return r
}

// read reads and normalizes m's series from mint to maxt; its budget charges what's decoded to the
// evaluations sharing it, as it arrives, and a panic becomes the read's error
func (c *RawCache) read(ctx context.Context, m Member, mint, maxt int64, hints *storage.SelectHints,
	matchers []*labels.Matcher, share func(int64) error,
) (r fetched) {
	safego.Run(func(p any, stack []byte) {
		r = fetched{err: fmt.Errorf("%w: %v", errSourcePanic, p)}
		logger.Error(memberPanicEvent, logging.Pairs{keys.Member: m.Name, keys.Panic: p, keys.Stack: string(stack)})
	}, func() {
		ctx = WithBudget(ctx, &Budget{
			MaxSeries: c.opts.MaxSeries, MaxSamples: c.opts.MaxSamples, MaxResponseBytes: c.opts.MaxResponseBytes,
			share: share,
		})
		var h *storage.SelectHints
		if hints != nil {
			copied := *hints
			copied.Start, copied.End = mint, maxt
			h = &copied
		}
		series, warnings, err := m.Source.Select(ctx, mint, maxt, h, matchers...)
		if err != nil {
			r = fetched{err: err}
			return
		}
		normalized, err := normalize(series, mint, maxt)
		r = fetched{series: normalized, warnings: warnings, err: err}
	})
	return r
}

// store keeps series, read completely from mint to maxt, in the blocks spanning them, and reports whether
// it kept them all
func (c *RawCache) store(key string, series []rawSeries, mint, maxt int64) bool {
	for b := blockIndex(mint); b <= blockIndex(maxt); b++ {
		bk := blockKey(key, b)
		mu := &c.locks[stripe(bk)]
		mu.Lock()
		err := c.storeBlock(bk, series, max(mint, b*rawBlockMs), min(maxt, (b+1)*rawBlockMs-1))
		mu.Unlock()
		if err != nil {
			logger.Debug("tsm global evaluation raw series not cached",
				logging.Pairs{keys.ALB_Name: c.opts.Name, keys.Error: err.Error()})
			return false
		}
	}
	return true
}

// storeBlock adds to the block at bk the samples from lo to hi it doesn't hold yet
func (c *RawCache) storeBlock(bk string, series []rawSeries, lo, hi int64) error {
	var current *rawBlock
	var covered timeseries.ExtentList
	if entry, ok := c.engine.Retrieve(bk); ok && !entry.Marker && entry.Payload != nil {
		// the entry may be shared, so its extents are copied before the delta sorts them
		current, covered = entry.Payload, entry.Extents.Clone()
	}
	add := covered.CalculateDeltas(timeseries.ExtentList{extent(lo, hi)}, time.Millisecond)
	if len(add) == 0 {
		return nil
	}
	ranges := make([][2]int64, len(add))
	for i, e := range add {
		ranges[i] = [2]int64{e.Start.UnixMilli(), e.End.UnixMilli()}
	}
	parts, err := cropSeries(series, ranges)
	if err != nil {
		return err
	}
	extents := covered.Merge(add, time.Millisecond)
	block := blockIndex(lo)
	complete := len(extents) == 1 && extents[0].Start.UnixMilli() == block*rawBlockMs &&
		extents[0].End.UnixMilli() == (block+1)*rawBlockMs-1
	c.engine.Store(bk, &nativedelta.Entry[*rawBlock]{
		Payload: mergeBlock(current, parts, extents, complete), Extents: extents,
	})
	return nil
}

// key returns the key of src's answers to matchers for ctx's request: src's identity, the request's identity
// headers as src sends them, and the matchers in order
func (c *RawCache) key(ctx context.Context, src Source, matchers []*labels.Matcher) (rawKey, bool) {
	id := src.Identity()
	if id == "" {
		return rawKey{}, false
	}
	h := sha256.New()
	writeKeyPart(h, rawCacheVersion)
	writeKeyPart(h, id)
	sent := sentHeaders(src, requestFrom(ctx))
	for _, name := range identityHeaders(ctx, src) {
		values := sent.Values(name)
		writeKeyPart(h, name)
		writeKeyPart(h, strconv.Itoa(len(values)))
		for _, v := range values {
			writeKeyPart(h, v)
		}
	}
	scope := h.Sum(nil)[:16]
	k := rawKey{scope: string(scope), matchers: slices.SortedFunc(slices.Values(matchers), compareMatchers)}
	h.Reset()
	h.Write(scope)
	for _, m := range k.matchers {
		writeKeyPart(h, m.Name)
		writeKeyPart(h, m.Type.String())
		writeKeyPart(h, m.Value)
		if k.metric == "" && m.Name == model.MetricNameLabel && m.Type == labels.MatchEqual {
			k.metric = m.Value
		}
	}
	k.key = rawKeyPrefix + hex.EncodeToString(h.Sum(nil)[:16])
	return k, true
}

// writeKeyPart writes s with its length first, so no two sequences of parts write the same bytes
func writeKeyPart(h hash.Hash, s string) {
	var n [binary.MaxVarintLen64]byte
	h.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
	h.Write([]byte(s))
}

func compareMatchers(a, b *labels.Matcher) int {
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if a.Type != b.Type {
		return int(a.Type) - int(b.Type)
	}
	return strings.Compare(a.Value, b.Value)
}

// identityHeaders returns the names of the headers partitioning src's cached answers: Authorization, those
// the request names, and those src's configuration keys by
func identityHeaders(ctx context.Context, src Source) []string {
	names := []string{headers.NameAuthorization}
	for _, list := range [][]string{keyHeadersFrom(ctx), src.KeyHeaders()} {
		for _, name := range list {
			if name = http.CanonicalHeaderKey(name); !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// headerSender is a source sending each read the client's headers, as a proxied request would carry them
type headerSender interface {
	sentHeaders(in *http.Request) http.Header
}

// sentHeaders returns the headers src sends a read for the client request in
func sentHeaders(src Source, in *http.Request) http.Header {
	if hs, ok := src.(headerSender); ok {
		return hs.sentHeaders(in)
	}
	if in == nil {
		return nil
	}
	return in.Header
}

// forwardedHeaders returns the headers a read for the client request in carries: its end-to-end headers,
// updated by the request headers paths sets for its path
func forwardedHeaders(paths po.List, in *http.Request) http.Header {
	if in == nil {
		return http.Header{}
	}
	h := in.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	headers.StripClientHeaders(h)
	if pc := paths.Match(in.Method, in.URL.Path); pc != nil && len(pc.RequestHeaders) > 0 {
		headers.UpdateHeaders(h, pc.RequestHeaders)
	}
	return h
}

func extent(mint, maxt int64) timeseries.Extent {
	return timeseries.Extent{Start: time.UnixMilli(mint), End: time.UnixMilli(maxt)}
}

// blockIndex returns the block holding t, counting from the epoch
func blockIndex(t int64) int64 {
	b := t / rawBlockMs
	if t%rawBlockMs < 0 {
		b--
	}
	return b
}

func blockKey(key string, b int64) string { return key + "." + strconv.FormatInt(b, 10) }

// stripe returns the lock stripe of a block key, by its FNV-1a hash
func stripe(key string) int {
	h := uint32(2166136261)
	for i := range len(key) {
		h = (h ^ uint32(key[i])) * 16777619
	}
	return int(h % rawLockStripes)
}

type keyHeadersKey struct{}

// WithKeyHeaders returns ctx carrying the names of the request headers that partition cached raw series,
// besides Authorization and each member's own.
func WithKeyHeaders(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, keyHeadersKey{}, names)
}

func keyHeadersFrom(ctx context.Context) []string {
	names, _ := ctx.Value(keyHeadersKey{}).([]string)
	return names
}

// flightCaller is a caller waiting on a shared read, with the budget the read charges for it
type flightCaller struct {
	budget *Budget
	// out closes when the read costs more than the caller's budget allows, as err says
	out chan struct{}
	err error
}

// flight is one run of a shared function and the callers waiting on it
type flight[T any] struct {
	done   chan struct{}
	result T
	cancel context.CancelFunc

	mu      sync.Mutex
	callers []*flightCaller
	// charged is what the run has charged its callers, which a caller joining later pays at once
	charged int64
	// ended is set once no caller is left, so the run stops and a later caller starts another
	ended    bool
	finished bool
}

// join adds c to the flight's callers, charging it what the run has read so far; it reports false when
// the flight has ended, and an error when c can't afford what it read
func (f *flight[T]) join(c *flightCaller) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return false, nil
	}
	if err := c.budget.add(0, f.charged); err != nil {
		return false, err
	}
	f.callers = append(f.callers, c)
	return true, nil
}

// charge charges samples the run read to each caller, releasing any whose budget they exceed; once none
// is left, it fails, so the run stops reading
func (f *flight[T]) charge(samples int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charged += samples
	kept := f.callers[:0]
	for _, c := range f.callers {
		if err := c.budget.add(0, samples); err != nil {
			c.err = err
			close(c.out)
			continue
		}
		kept = append(kept, c)
	}
	clear(f.callers[len(kept):])
	f.callers = kept
	if len(kept) == 0 {
		f.ended = true
		f.cancel()
		return fmt.Errorf("%w: no evaluation sharing the read can afford more", ErrFetchLimit)
	}
	return nil
}

// leave removes c, which no longer waits; an unfinished flight no caller waits on ends
func (f *flight[T]) leave(c *flightCaller) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := slices.Index(f.callers, c); i >= 0 {
		f.callers = slices.Delete(f.callers, i, i+1)
	}
	if len(f.callers) == 0 && !f.finished {
		f.ended = true
		f.cancel()
	}
}

// flightGroup runs a function once for a key's concurrent callers, with the first's context values; it ends
// only when every caller has gone, so none fails another
type flightGroup[T any] struct {
	mu      sync.Mutex
	flights map[string]*flight[T]
	// running counts flights until their after functions return
	running sync.WaitGroup
}

// do returns fn's result for key, run once for concurrent callers each charged to its budget, or what ended
// the caller's wait; after runs once callers have the result
func (g *flightGroup[T]) do(ctx context.Context, key string, budget *Budget,
	fn func(context.Context, func(int64) error) T, after func(T),
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	caller := &flightCaller{budget: budget, out: make(chan struct{})}
	g.mu.Lock()
	if g.flights == nil {
		g.flights = make(map[string]*flight[T])
	}
	f, joined := g.flights[key], false
	if f != nil {
		var err error
		if joined, err = f.join(caller); err != nil {
			g.mu.Unlock()
			return zero, err
		}
	}
	if !joined {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // the flight's run calls it
		f = &flight[T]{done: make(chan struct{}), cancel: cancel, callers: []*flightCaller{caller}}
		g.flights[key] = f
		g.running.Add(1)
		go g.run(fctx, key, f, fn, after)
	}
	g.mu.Unlock()
	select {
	case <-f.done:
		// a caller the read outgrew fails, even when the read then finished for others
		select {
		case <-caller.out:
			return zero, caller.err
		default:
			return f.result, nil
		}
	case <-caller.out:
		return zero, caller.err
	case <-ctx.Done():
		f.leave(caller)
		return zero, ctx.Err()
	}
}

func (g *flightGroup[T]) run(ctx context.Context, key string, f *flight[T], fn func(context.Context, func(int64) error) T,
	after func(T),
) {
	defer g.running.Done()
	defer f.cancel()
	f.result = fn(ctx, f.charge)
	f.mu.Lock()
	f.finished = true
	f.mu.Unlock()
	close(f.done)
	if after != nil {
		after(f.result)
	}
	g.mu.Lock()
	if g.flights[key] == f {
		delete(g.flights, key)
	}
	g.mu.Unlock()
}
