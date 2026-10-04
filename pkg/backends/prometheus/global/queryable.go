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
	"errors"
	"fmt"
	"sync"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
)

// ErrMemberUnavailable is returned or warned when a member's raw series can't be read.
var ErrMemberUnavailable = errors.New("tsm global evaluation member unavailable")

// Member is a pool member read by one evaluation; members are listed in configured order.
type Member struct {
	Name   string
	Source Source
}

// Queryable presents the deduplicated raw series of every member to the Prometheus engine as one TSDB.
type Queryable struct {
	members       []Member
	replicaLabels []string
	deduplication string
	partial       bool
	failed        func(member string)
}

// NewQueryable returns a queryable over members; a member that can't be read is left out with a
// warning when partial is true and fails the query otherwise, and failed learns of it.
func NewQueryable(members []Member, replicaLabels []string, deduplication string, partial bool,
	failed func(member string),
) *Queryable {
	return &Queryable{
		members: members, replicaLabels: replicaLabels, deduplication: deduplication,
		partial: partial, failed: failed,
	}
}

// Querier returns a querier reading samples from mint to maxt unless select hints narrow them.
func (q *Queryable) Querier(mint, maxt int64) (storage.Querier, error) {
	return &querier{q: q, mint: mint, maxt: maxt}, nil
}

type querier struct {
	q          *Queryable
	mint, maxt int64
}

// Select reads every member concurrently and deduplicates what they return. Series are always sorted.
func (qr *querier) Select(ctx context.Context, _ bool, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) storage.SeriesSet {
	mint, maxt, function := qr.mint, qr.maxt, ""
	if hints != nil {
		mint, maxt, function = hints.Start, hints.End, hints.Func
	}
	members := qr.q.members
	results := make([][]storage.Series, len(members))
	errs := make([]error, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Go(func() { results[i], errs[i] = m.Source.Select(ctx, mint, maxt, hints, matchers...) })
	}
	wg.Wait()
	var warnings annotations.Annotations
	for i, err := range errs {
		if err == nil {
			continue
		}
		if qr.q.failed != nil {
			qr.q.failed(members[i].Name)
		}
		err = fmt.Errorf("%w: %s: %w", ErrMemberUnavailable, members[i].Name, err)
		// a limit or cancellation ends the query rather than shrinking its answer
		if !qr.q.partial || errors.Is(err, ErrFetchLimit) || ctx.Err() != nil {
			return storage.ErrSeriesSet(err)
		}
		warnings.Add(err)
		results[i] = nil
	}
	return &seriesSet{
		series:   Deduplicate(results, qr.q.replicaLabels, qr.q.deduplication, function),
		warnings: warnings,
	}
}

// LabelValues is not used to evaluate queries, so the queryable serves no label values.
func (*querier) LabelValues(context.Context, string, *storage.LabelHints,
	...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

// LabelNames is not used to evaluate queries, so the queryable serves no label names.
func (*querier) LabelNames(context.Context, *storage.LabelHints,
	...*labels.Matcher,
) ([]string, annotations.Annotations, error) {
	return nil, nil, nil
}

func (*querier) Close() error { return nil }

type seriesSet struct {
	series   []storage.Series
	next     int
	warnings annotations.Annotations
}

func (s *seriesSet) Next() bool {
	s.next++
	return s.next <= len(s.series)
}

func (s *seriesSet) At() storage.Series                { return s.series[s.next-1] }
func (s *seriesSet) Err() error                        { return nil }
func (s *seriesSet) Warnings() annotations.Annotations { return s.warnings }
