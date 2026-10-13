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

// Package global reads the raw series of Prometheus-compatible members and evaluates PromQL once over
// them, for time series merge ALBs whose evaluation is global.
package global

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
)

// Source reads a member's raw series for the Prometheus engine.
type Source interface {
	backends.TSMGlobalSource
	// VolatileWindow is how far back from now the member's samples may still change, so a cache reads them again
	VolatileWindow() time.Duration
	// Select returns the member's series that match every matcher, with their samples from mint to maxt,
	// and any warnings the member gave that its answer may be incomplete
	Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
		matchers ...*labels.Matcher) ([]storage.Series, annotations.Annotations, error)
}

// SourceIdentity is what a source reports about its answers besides the query: what decides them, and
// how long they may still change.
type SourceIdentity struct {
	// ID digests where and how the source reads, with the headers and credentials it sends
	ID string
	// Headers are the request headers the member's configuration keys its cached answers by
	Headers []string
	// Volatile is how far back from now the member's samples may still change
	Volatile time.Duration
}

// Identity returns the digest of where and how the source reads.
func (i SourceIdentity) Identity() string { return i.ID }

// KeyHeaders returns the request headers the member's configuration keys its cached answers by.
func (i SourceIdentity) KeyHeaders() []string { return i.Headers }

// VolatileWindow returns how far back from now the member's samples may still change.
func (i SourceIdentity) VolatileWindow() time.Duration { return i.Volatile }

// ErrFetchLimit is returned when an evaluation retrieves more raw data than its budget allows.
var ErrFetchLimit = errors.New("tsm global evaluation exceeded its raw data limit")

// Budget bounds the raw series and samples one evaluation reads within its selectors' ranges, from its
// sources or the raw cache; zero limits are unlimited.
type Budget struct {
	MaxSeries        int64
	MaxSamples       int64
	MaxResponseBytes int64

	series  atomic.Int64
	samples atomic.Int64
	// share, set on a shared read's own budget, charges its series and samples to every evaluation it reads for
	share shareFunc
}

// shareFunc charges series and samples a shared read added to its budget to the evaluations it reads for; ids are the
// label set hashes of the series, when the budget identifies them
type shareFunc func(series, samples int64, ids []uint64) error

// add counts series that have samples in range and the samples, failing once either exceeds its limit
func (b *Budget) add(series, samples int64) error { return b.addSeries(series, samples, nil) }

// addSeries is add for series whose label set hashes are ids, when the budget identifies them, which a shared read
// passes on, so an evaluation reading a series in several of a selector's time ranges counts it once
func (b *Budget) addSeries(series, samples int64, ids []uint64) error {
	if b == nil {
		return nil
	}
	if total := b.series.Add(series); b.MaxSeries > 0 && total > b.MaxSeries {
		return b.seriesLimitError()
	}
	if total := b.samples.Add(samples); b.MaxSamples > 0 && total > b.MaxSamples {
		return b.samplesLimitError()
	}
	if b.share != nil && (series > 0 || samples > 0) {
		return b.share(series, samples, ids)
	}
	return nil
}

// check returns the error adding series and samples would fail with, without adding them, so a reader can stop
// before keeping what it can't afford, then add all it kept at once
func (b *Budget) check(series, samples int64) error {
	switch {
	case b == nil:
		return nil
	case b.MaxSeries > 0 && b.series.Load()+series > b.MaxSeries:
		return b.seriesLimitError()
	case b.MaxSamples > 0 && b.samples.Load()+samples > b.MaxSamples:
		return b.samplesLimitError()
	}
	return nil
}

func (b *Budget) seriesLimitError() error {
	return fmt.Errorf("%w: more than %d series", ErrFetchLimit, b.MaxSeries)
}

func (b *Budget) samplesLimitError() error {
	return fmt.Errorf("%w: more than %d samples", ErrFetchLimit, b.MaxSamples)
}

// identifies reports whether series are counted by their label sets' hashes, so a copy of one isn't counted again:
// only when their number is limited, as a hash set as large as the limit is then all that's kept
func (b *Budget) identifies() bool { return b != nil && b.MaxSeries > 0 }

// exceeds reports whether adding series and samples would exceed either limit, reading the counts without
// adding to them, so a reader can check each chunk before keeping it and add many at once
func (b *Budget) exceeds(series, samples int64) bool {
	return b != nil && ((b.MaxSeries > 0 && b.series.Load()+series > b.MaxSeries) ||
		(b.MaxSamples > 0 && b.samples.Load()+samples > b.MaxSamples))
}

// Fetched returns the series and samples the budget has counted.
func (b *Budget) Fetched() (series, samples int64) {
	return b.series.Load(), b.samples.Load()
}

// responseLimit returns the most bytes a source reads for one response, or zero for no limit
func (b *Budget) responseLimit() int64 {
	if b == nil {
		return 0
	}
	return b.MaxResponseBytes
}

// decodeLimit returns the most bytes the series kept from one response may take: decodeExpansion times its byte
// limit, or the default capture limit's when it has none, so decoding is bounded even when fetching isn't
func (b *Budget) decodeLimit() int64 {
	limit := b.responseLimit()
	if limit <= 0 {
		limit = bo.DefaultMaxCaptureBytes
	}
	if limit > math.MaxInt64/decodeExpansion {
		return math.MaxInt64
	}
	return decodeExpansion * limit
}

type requestKey struct{}

type budgetKey struct{}

type replicaLabelsKey struct{}

// WithRequest returns ctx carrying the client request whose credentials and tenant headers sources forward.
func WithRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, requestKey{}, r)
}

// WithBudget returns ctx carrying the budget that bounds what sources retrieve.
func WithBudget(ctx context.Context, b *Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}

func budgetFrom(ctx context.Context) *Budget {
	b, _ := ctx.Value(budgetKey{}).(*Budget)
	return b
}

// withReplicaLabels returns ctx carrying the labels deduplication groups series without, so a source decoding a
// response charges what each group's series lay out together, as deduplication reads them
func withReplicaLabels(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, replicaLabelsKey{}, names)
}

func replicaLabelsFrom(ctx context.Context) []string {
	names, _ := ctx.Value(replicaLabelsKey{}).([]string)
	return names
}
