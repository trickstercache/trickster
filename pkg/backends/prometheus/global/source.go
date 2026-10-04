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
	"net/http"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
)

// Source reads a member's raw series for the Prometheus engine.
type Source interface {
	backends.TSMGlobalSource
	// Select returns the member's series that match every matcher, with their samples from mint to maxt
	Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
		matchers ...*labels.Matcher) ([]storage.Series, error)
}

// ErrFetchLimit is returned when an evaluation retrieves more raw data than its budget allows.
var ErrFetchLimit = errors.New("tsm global evaluation exceeded its raw data limit")

// Budget bounds the raw data one evaluation retrieves from all of its sources; zero limits are unlimited.
type Budget struct {
	MaxSeries        int64
	MaxSamples       int64
	MaxResponseBytes int64

	series  atomic.Int64
	samples atomic.Int64
}

// add counts retrieved series and samples, failing once either exceeds its limit
func (b *Budget) add(series, samples int64) error {
	if b == nil {
		return nil
	}
	if total := b.series.Add(series); b.MaxSeries > 0 && total > b.MaxSeries {
		return fmt.Errorf("%w: more than %d series", ErrFetchLimit, b.MaxSeries)
	}
	if total := b.samples.Add(samples); b.MaxSamples > 0 && total > b.MaxSamples {
		return fmt.Errorf("%w: more than %d samples", ErrFetchLimit, b.MaxSamples)
	}
	return nil
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

type requestKey struct{}

type budgetKey struct{}

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
