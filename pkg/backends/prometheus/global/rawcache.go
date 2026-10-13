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
	"maps"
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
	// rawStripeRefusals bounds the blocks refused for their size that each lock stripe remembers
	rawStripeRefusals = 64
	// rawGapReads bounds how many of one read's missing extents are read from the member at once
	rawGapReads = 4
	// memberPanicEvent is logged when reading or decoding a member's answer panics
	memberPanicEvent = "tsm global evaluation member read panicked"
	// storePanicEvent is logged when keeping a read's series in a block panics, as a malformed chunk can
	storePanicEvent = "tsm global evaluation raw series block store panicked"
	// flightPanicEvent is logged when what a shared read does once its callers have the result panics
	flightPanicEvent = "tsm global evaluation shared read callback panicked"
)

var errStorePanic = errors.New("raw series block store panicked")

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
	// MaxObjectSize, when positive, is the most bytes a block may take in the cache: a larger block isn't kept, nor tried again
	// for the same samples while it holds what it did, until TTL passes. Blocks are keyed by it, so caches of other limits keep their own
	MaxObjectSize int64
	// MaxSeries, MaxSamples and MaxResponseBytes bound each member read, as an evaluation's Budget does
	MaxSeries, MaxSamples, MaxResponseBytes int64
	// Observe, when set, learns the cache status of each member read: hit, phit, kmiss or proxy-only
	Observe func(status cachestatus.LookupStatus)
	// Subsumed, when set, learns of each member read answered in part from a broader selector's blocks
	Subsumed func()
	// ObserveCacheFailure, when set, learns why the cache failed to keep or read a block, as a reason prefixed
	// by tsm_global_raw_, such as tsm_global_raw_max_object_size
	ObserveCacheFailure func(reason string)
}

// RawCache keeps members' raw series in a cache, in blocks of time keyed by size limit, member, request identity
// and matchers, so a member is read only for what it lacks and the volatile window.
type RawCache struct {
	opts    RawCacheOptions
	engine  *nativedelta.Engine[*rawBlock]
	flights flightGroup[fetched]
	// locks serialize storing each block, by its key's stripe, so no store loses another's samples
	locks [rawLockStripes]sync.Mutex
	// refused remembers, by stripe and under its lock, the samples each block was last refused for its size
	refused [rawLockStripes]map[string]rawRefusal
	// keyScope is what every key's hash starts with: the cache version and the size limit, each as writeKeyPart writes it
	keyScope []byte
	// index remembers which selectors' blocks are cached, so a narrower selector can read a broader one's
	index rawIndex
	now   func() time.Time
}

// rawRefusal is the range of samples a block couldn't keep within the size limit, until expires, merged onto the
// extents the block then held, which are none when it held nothing
type rawRefusal struct {
	lo, hi  int64
	onto    timeseries.ExtentList
	expires time.Time
}

// heldExtents are the extents each of a select's own blocks held when it looked them up, from block first on
type heldExtents struct {
	first   int64
	extents []timeseries.ExtentList
}

// of returns the extents block b held, which are none when it wasn't cached
func (h heldExtents) of(b int64) timeseries.ExtentList {
	if i := b - h.first; i >= 0 && i < int64(len(h.extents)) {
		return h.extents[i]
	}
	return nil
}

// holds reports whether every extent of want lies within one of held's, which are merged
func holds(held, want timeseries.ExtentList) bool {
	for _, w := range want {
		if !slices.ContainsFunc(held, func(h timeseries.Extent) bool {
			return !w.Start.Before(h.Start) && !w.End.After(h.End)
		}) {
			return false
		}
	}
	return true
}

// NewRawCache returns a RawCache keeping blocks in c.
func NewRawCache(c cache.Cache, opts RawCacheOptions) *RawCache {
	rc := &RawCache{opts: opts, now: time.Now}
	for _, part := range []string{rawCacheVersion, strconv.FormatInt(max(opts.MaxObjectSize, 0), 10)} {
		rc.keyScope = binary.AppendUvarint(rc.keyScope, uint64(len(part)))
		rc.keyScope = append(rc.keyScope, part...)
	}
	var failed func(string)
	if observe := opts.ObserveCacheFailure; observe != nil {
		failed = func(reason string) { observe(rawCacheProtocol + "_" + reason) }
	}
	rc.engine = nativedelta.New[*rawBlock](nativedelta.Config{
		Protocol:            rawCacheProtocol,
		BackendName:         opts.Name,
		CacheClient:         func() cache.Cache { return c },
		CacheTTL:            opts.TTL,
		MaxObjectSize:       opts.MaxObjectSize,
		ObserveCacheFailure: failed,
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
	sources, covered, held, subsumed := c.cached(k, max(mint, keep.mint), min(maxt, keep.maxt), now)
	if subsumed && c.opts.Subsumed != nil {
		c.opts.Subsumed()
	}
	missing := covered.CalculateDeltas(timeseries.ExtentList{extent(mint, maxt)}, time.Millisecond)
	// reads limited in series identify them, so a select reading several ranges can charge each series once as it arrives
	charge := newSelectCharge(budgetFrom(ctx), len(missing) > 1 && c.opts.MaxSeries > 0)
	results, err := c.fetchAll(ctx, k, m, missing, keep, held, hints, matchers, charge)
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
	// the member's reads charged what they held as it arrived; the rest of the series served, and cached samples, are now
	if err := charge.settle(int64(len(series)), cachedSamples); err != nil {
		return nil, nil, err
	}
	return series, warnings, nil
}

// fetchAll reads the gaps from m, a few at a time; the first failure, as the evaluation running out of budget, cancels the
// reads not yet done. A read that reached a fetch limit decides the error, even when ctx ended meanwhile
func (c *RawCache) fetchAll(ctx context.Context, k rawKey, m Member, gaps timeseries.ExtentList,
	keep span, held heldExtents, hints *storage.SelectHints, matchers []*labels.Matcher, charge *selectCharge,
) ([]fetched, error) {
	if len(gaps) == 0 {
		// the cache held every sample, so nothing is read
		return nil, ctx.Err()
	}
	results := make([]fetched, len(gaps))
	if len(gaps) == 1 {
		results[0] = c.fetch(ctx, k, m, gaps[0], keep, held, hints, matchers, charge)
		return results, results[0].err
	}
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(gaps), rawGapReads) {
		wg.Go(func() {
			for i := int(next.Add(1)) - 1; i < len(gaps) && gctx.Err() == nil; i = int(next.Add(1)) - 1 {
				if results[i] = c.fetch(gctx, k, m, gaps[i], keep, held, hints, matchers, charge); results[i].err != nil {
					cancel()
					return
				}
			}
		})
	}
	wg.Wait()
	var limits, failures []error
	for _, r := range results {
		switch {
		case r.err == nil:
		case errors.Is(r.err, ErrFetchLimit):
			limits = append(limits, r.err)
		case !errors.Is(r.err, context.Canceled):
			// a read this select canceled after another failed reports only that failure
			failures = append(failures, r.err)
		}
	}
	switch {
	case len(limits) > 0:
		return nil, errors.Join(limits...)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case len(failures) > 0:
		return nil, errors.Join(failures...)
	case gctx.Err() != nil:
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

// cached returns the cached blocks holding samples of k from mint to maxt, with the extents they cover, and what
// k's own blocks held. What they lack is taken from broader selectors' blocks holding it; subsumed reports any did.
func (c *RawCache) cached(k rawKey, mint, maxt int64, now time.Time,
) (sources []rawSource, covered timeseries.ExtentList, held heldExtents, subsumed bool) {
	if mint > maxt {
		return nil, nil, held, false
	}
	var candidates []rawCandidate
	looked, own := false, false
	last := blockIndex(maxt)
	for b := blockIndex(mint); b <= last; b++ {
		lo, hi := max(mint, b*rawBlockMs), min(maxt, (b+1)*rawBlockMs-1)
		have, all := c.cachedBlock(&sources, k.key, b, lo, hi, nil, nil)
		if len(all) > 0 {
			if held.extents == nil {
				held = heldExtents{first: b, extents: make([]timeseries.ExtentList, last-b+1)}
			}
			held.extents[b-held.first] = all
		}
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
			if got, _ := c.cachedBlock(&sources, candidates[i].key, b, lo, hi, need, candidates[i].extra); len(got) > 0 {
				have, subsumed = append(have, got...), true
			}
		}
		covered = append(covered, have...)
	}
	if own {
		c.index.record(k, now, c.opts.TTL)
	}
	return sources, covered, held, subsumed
}

// cachedBlock adds the source of block b of key from lo to hi to sources, returning the extents it covers, and all
// it holds. A broader selector's block adds only what need names, each part its own source, so no sample is read twice.
func (c *RawCache) cachedBlock(sources *[]rawSource, key string, b, lo, hi int64, need timeseries.ExtentList,
	match []*labels.Matcher,
) (got, all timeseries.ExtentList) {
	bk := blockKey(key, b)
	entry, ok := c.engine.Retrieve(bk)
	if !ok || entry.Marker || entry.Payload == nil {
		return nil, nil
	}
	// what a block holds past hi, as after the volatile window has grown, is read again
	e := entry.Extents.Crop(extent(lo, hi))
	if need == nil {
		if len(e) > 0 {
			*sources = append(*sources, rawSource{series: entry.Payload.series, lo: lo, hi: hi, block: bk})
		}
		return e, entry.Extents
	}
	for _, n := range need {
		for _, part := range e.Crop(n) {
			got = append(got, part)
			*sources = append(*sources, rawSource{
				series: entry.Payload.series, lo: part.Start.UnixMilli(),
				hi: part.End.UnixMilli(), block: bk, match: match,
			})
		}
	}
	return got, entry.Extents
}

// fetch reads m's series within gap, sharing the read with concurrent callers asking for the same, each paying for it
// through its own charge; it keeps what lies within keep once the member answered completely, into blocks that held what held lists
func (c *RawCache) fetch(ctx context.Context, k rawKey, m Member, gap timeseries.Extent, keep span, held heldExtents,
	hints *storage.SelectHints, matchers []*labels.Matcher, charge *selectCharge,
) fetched {
	mint, maxt := gap.Start.UnixMilli(), gap.End.UnixMilli()
	flight := k.key + "/" + strconv.FormatInt(mint, 10) + "/" + strconv.FormatInt(maxt, 10)
	r, err := c.flights.do(ctx, flight, charge, func(ctx context.Context, share shareFunc) fetched {
		return c.read(ctx, m, mint, maxt, hints, matchers, share)
	}, func(r fetched) {
		if lo, hi := max(mint, keep.mint), min(maxt, keep.maxt); r.err == nil && len(r.warnings) == 0 && lo <= hi &&
			c.store(k.key, r.series, lo, hi, held) {
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
	matchers []*labels.Matcher, share shareFunc,
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

// store keeps series, read completely from mint to maxt, in the blocks spanning them, which held what held lists as the read
// began, and reports whether any block holds them. A block they'd take past the size limit is passed over; any other failure fails.
func (c *RawCache) store(key string, series []rawSeries, mint, maxt int64, held heldExtents) bool {
	kept := false
	for b := blockIndex(mint); b <= blockIndex(maxt); b++ {
		lo, hi := max(mint, b*rawBlockMs), min(maxt, (b+1)*rawBlockMs-1)
		switch err := c.keepBlock(blockKey(key, b), series, lo, hi, held.of(b)); {
		case err == nil:
			kept = true
		case !errors.Is(err, nativedelta.ErrObjectTooLarge):
			logger.Debug("tsm global evaluation raw series not cached",
				logging.Pairs{keys.ALB_Name: c.opts.Name, keys.Error: err.Error()})
			return false
		}
	}
	return kept
}

// keepBlock stores into the block at bk, which held the extents held as the read began, under its stripe's lock. A panic, as
// decoding a malformed chunk can raise, fails the store; one raised once the block's own chunks are read drops the block too.
func (c *RawCache) keepBlock(bk string, series []rawSeries, lo, hi int64, held timeseries.ExtentList) (err error) {
	s := stripe(bk)
	mu := &c.locks[s]
	mu.Lock()
	defer mu.Unlock()
	// samples a block was refused for, or more, are refused again without reading or merging the block until TTL passes,
	// while it holds at least what they were merged onto; one it no longer holds, as once evicted, may take them now
	if r, ok := c.refused[s][bk]; ok && lo <= r.lo && hi >= r.hi && c.now().Before(r.expires) && holds(held, r.onto) {
		return nativedelta.ErrObjectTooLarge
	}
	var merging bool
	var onto timeseries.ExtentList
	safego.Run(func(p any, stack []byte) {
		if merging {
			c.engine.Remove(bk)
		}
		err = fmt.Errorf("%w: %v", errStorePanic, p)
		logger.Error(storePanicEvent, logging.Pairs{
			keys.ALB_Name: c.opts.Name, keys.Key: bk, keys.Panic: p, keys.Stack: string(stack),
		})
	}, func() {
		onto, err = c.storeBlock(bk, series, lo, hi, &merging)
	})
	if errors.Is(err, nativedelta.ErrObjectTooLarge) {
		c.refuse(s, bk, lo, hi, onto)
	}
	return err
}

// refuse remembers that the block at bk, of stripe s whose lock is held, couldn't keep the samples from lo to hi
// besides the extents onto within the size limit; a full stripe forgets its expired refusals, else any one
func (c *RawCache) refuse(s int, bk string, lo, hi int64, onto timeseries.ExtentList) {
	now := c.now()
	refused := c.refused[s]
	if refused == nil {
		refused = make(map[string]rawRefusal)
		c.refused[s] = refused
	}
	if _, ok := refused[bk]; !ok && len(refused) >= rawStripeRefusals {
		maps.DeleteFunc(refused, func(_ string, r rawRefusal) bool { return !now.Before(r.expires) })
		for k := range refused {
			if len(refused) < rawStripeRefusals {
				break
			}
			delete(refused, k)
		}
	}
	refused[bk] = rawRefusal{lo: lo, hi: hi, onto: onto, expires: now.Add(c.opts.TTL)}
}

// storeBlock adds to the block at bk the samples from lo to hi it doesn't hold yet, returning the extents it held
// before; merging is set once it reads the block's own chunks, not only series'
func (c *RawCache) storeBlock(bk string, series []rawSeries, lo, hi int64, merging *bool,
) (covered timeseries.ExtentList, err error) {
	var current *rawBlock
	if entry, ok := c.engine.Retrieve(bk); ok && !entry.Marker && entry.Payload != nil {
		// the entry may be shared, so its extents are copied before the delta sorts them
		current, covered = entry.Payload, entry.Extents.Clone()
	}
	add := covered.CalculateDeltas(timeseries.ExtentList{extent(lo, hi)}, time.Millisecond)
	if len(add) == 0 {
		return covered, nil
	}
	ranges := make([][2]int64, len(add))
	for i, e := range add {
		ranges[i] = [2]int64{e.Start.UnixMilli(), e.End.UnixMilli()}
	}
	parts, err := cropSeries(series, ranges)
	if err != nil {
		return covered, err
	}
	extents := covered.Merge(add, time.Millisecond)
	block := blockIndex(lo)
	complete := len(extents) == 1 && extents[0].Start.UnixMilli() == block*rawBlockMs &&
		extents[0].End.UnixMilli() == (block+1)*rawBlockMs-1
	*merging = current != nil
	return covered, c.engine.Keep(bk, &nativedelta.Entry[*rawBlock]{
		Payload: mergeBlock(current, parts, extents, complete), Extents: extents,
	})
}

// key returns the key of src's answers to matchers for ctx's request: the size limit, so caches of other limits
// never drop each other's blocks, src's identity, the request's identity headers as src sends them, and the matchers
func (c *RawCache) key(ctx context.Context, src Source, matchers []*labels.Matcher) (rawKey, bool) {
	id := src.Identity()
	if id == "" {
		return rawKey{}, false
	}
	h := sha256.New()
	h.Write(c.keyScope)
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

// selectCharge charges one select's reads to its evaluation's budget. Their ranges are disjoint but share series: it
// charges each by its id once, when identify is set, else the most any one read has held; settle charges the rest last.
type selectCharge struct {
	budget *Budget
	// identify is set when the select counts its series by their ids, in seen
	identify bool
	mu       sync.Mutex
	// series is what the budget has been charged for the select's series so far
	series int64
	// seen holds the ids of the series the select's reads have reported, never more than one past the budget's limit
	seen map[uint64]struct{}
}

// newSelectCharge returns the charge of a select of budget, which counts series by their ids when identified is set:
// its several reads identify series, and the budget limits them
func newSelectCharge(budget *Budget, identified bool) *selectCharge {
	return &selectCharge{budget: budget, identify: identified && budget.identifies()}
}

// pay charges samples, and raises what's charged for series to readSeries, all those one read has held so far, or
// when the select counts ids, to every distinct one its reads have reported, ids the latest, if that is more
func (s *selectCharge) pay(readSeries, samples int64, ids []uint64) error {
	if err := s.budget.add(0, samples); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identify {
		if s.seen == nil && len(ids) > 0 {
			// sized at once for the first ids, which a late caller's payment for what a shared read held can make many
			s.seen = make(map[uint64]struct{}, min(int64(len(ids)), s.budget.MaxSeries+1))
		}
		// once the set holds more series than the budget allows, the charge below fails, so it grows no further
		for _, id := range ids {
			if int64(len(s.seen)) > s.budget.MaxSeries {
				break
			}
			s.seen[id] = struct{}{}
		}
		readSeries = max(readSeries, int64(len(s.seen)))
	}
	if readSeries <= s.series {
		return nil
	}
	n := readSeries - s.series
	s.series = readSeries
	return s.budget.add(n, 0)
}

// settle charges the cached samples the select serves, and the series it serves that its reads' charges left out, or
// takes back those they charged but it doesn't serve, as copies of a series a read held apart
func (s *selectCharge) settle(served, cachedSamples int64) error {
	s.mu.Lock()
	charged := s.series
	s.mu.Unlock()
	return s.budget.add(served-charged, cachedSamples)
}

// flightCaller is a caller waiting on a shared read, with what the read charges for it
type flightCaller struct {
	charge *selectCharge
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
	// series and samples are what the run has read, and ids the series' ids when its read identifies them, as it does only
	// under a series limit, which bounds them; ids is only appended to, so a caller joining later pays them unlocked
	series, samples int64
	ids             []uint64
	// ended is set once no caller is left, so the run stops and a later caller starts another
	ended    bool
	finished bool
}

// join adds c to the flight's callers, returning the series, with their ids, and samples the run has read so far, for c
// to pay once no lock is held; ok is false when the flight has ended
func (f *flight[T]) join(c *flightCaller) (series, samples int64, ids []uint64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return 0, 0, nil, false
	}
	f.callers = append(f.callers, c)
	return f.series, f.samples, f.ids[:len(f.ids):len(f.ids)], true
}

// charge charges the series, with their ids, and samples the run read to each caller, releasing any whose budget
// they exceed; once none is left, it fails, so the run stops reading
func (f *flight[T]) charge(series, samples int64, ids []uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.series += series
	f.samples += samples
	f.ids = append(f.ids, ids...)
	kept := f.callers[:0]
	for _, c := range f.callers {
		if err := c.charge.pay(f.series, samples, ids); err != nil {
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

// leave removes c, which no longer waits, reporting whether the run had finished; an unfinished flight no caller waits on ends.
// It waits out any charge under way, so once it returns, no charge closes c.out but one that already has
func (f *flight[T]) leave(c *flightCaller) (finished bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := slices.Index(f.callers, c); i >= 0 {
		f.callers = slices.Delete(f.callers, i, i+1)
	}
	if len(f.callers) == 0 && !f.finished {
		f.ended = true
		f.cancel()
	}
	return f.finished
}

// flightGroup runs a function once for a key's concurrent callers, with the first's context values; it ends
// only when every caller has gone, so none fails another
type flightGroup[T any] struct {
	mu      sync.Mutex
	flights map[string]*flight[T]
	// running counts flights until their after functions return
	running sync.WaitGroup
}

// do returns fn's result for key, run once for concurrent callers each paying through its own charge, or what
// ended the caller's wait; after runs once callers have the result
func (g *flightGroup[T]) do(ctx context.Context, key string, charge *selectCharge,
	fn func(context.Context, shareFunc) T, after func(T),
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	caller := &flightCaller{charge: charge, out: make(chan struct{})}
	g.mu.Lock()
	if g.flights == nil {
		g.flights = make(map[string]*flight[T])
	}
	var series, samples int64
	var ids []uint64
	f, joined := g.flights[key], false
	if f != nil {
		series, samples, ids, joined = f.join(caller)
	}
	if !joined {
		fctx, cancel := context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // the flight's run calls it
		f = &flight[T]{done: make(chan struct{}), cancel: cancel, callers: []*flightCaller{caller}}
		g.flights[key] = f
		g.running.Add(1)
		go g.run(fctx, key, f, fn, after)
	}
	g.mu.Unlock()
	// a caller joining late pays for what the run read before, unlocked, as that can be every series' id; the run's
	// charges meanwhile commute with it, as series charged are the union or most of what's read, and samples a sum
	if joined {
		if err := charge.pay(series, samples, ids); err != nil {
			f.leave(caller)
			return zero, err
		}
	}
	select {
	case <-f.done:
	case <-caller.out:
	case <-ctx.Done():
		// a read that finished, or outgrew the caller, by the time it left answers with that outcome rather than ctx's end
		if !f.leave(caller) {
			select {
			case <-caller.out:
			default:
				return zero, ctx.Err()
			}
		}
	}
	// a caller the read outgrew fails, even when the read then finished for others
	select {
	case <-caller.out:
		return zero, caller.err
	default:
		return f.result, nil
	}
}

// run runs fn for f's callers, then after; f stays joinable until after returns, and is then forgotten and
// counted done even when after panics, which its callers, already answered, never see
func (g *flightGroup[T]) run(ctx context.Context, key string, f *flight[T],
	fn func(context.Context, shareFunc) T, after func(T),
) {
	defer g.running.Done()
	defer g.forget(key, f)
	defer f.cancel()
	f.result = fn(ctx, f.charge)
	f.mu.Lock()
	f.finished = true
	f.mu.Unlock()
	close(f.done)
	if after != nil {
		safego.Run(logFlightPanic, func() { after(f.result) })
	}
}

// forget removes f from the group, unless another flight has since taken its key
func (g *flightGroup[T]) forget(key string, f *flight[T]) {
	g.mu.Lock()
	if g.flights[key] == f {
		delete(g.flights, key)
	}
	g.mu.Unlock()
}

func logFlightPanic(p any, stack []byte) {
	logger.Error(flightPanicEvent, logging.Pairs{keys.Panic: p, keys.Stack: string(stack)})
}
