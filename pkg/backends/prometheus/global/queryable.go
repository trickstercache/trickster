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
	failed        func(member string, err error)
}

// NewQueryable returns a queryable over members; a member that can't be read is left out with a
// warning when partial is true and fails the query otherwise, and failed learns why.
func NewQueryable(members []Member, replicaLabels []string, deduplication string, partial bool,
	failed func(member string, err error),
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
	incomplete := make([]annotations.Annotations, len(members))
	errs := make([]error, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Go(func() { results[i], incomplete[i], errs[i] = m.Source.Select(ctx, mint, maxt, hints, matchers...) })
	}
	wg.Wait()
	var warnings annotations.Annotations
	for i, err := range errs {
		name := members[i].Name
		var clientErr error
		switch {
		case err == nil && len(incomplete[i]) == 0:
			continue
		case err == nil:
			// an incomplete answer is kept when partial responses are allowed, with the member's own warnings
			clientErr = fmt.Errorf("%w: %s: incomplete answer: %w", ErrMemberUnavailable, name,
				errors.Join(incomplete[i].AsErrors()...))
			if qr.q.partial {
				warnings.Add(clientErr)
				continue
			}
			err = clientErr
		case errors.Is(err, ErrFetchLimit):
			clientErr = fmt.Errorf("%w: %s: %w", ErrMemberUnavailable, name, err)
		case ctx.Err() != nil:
			clientErr = fmt.Errorf("%w: %s: %w", ErrMemberUnavailable, name, ctx.Err())
		default:
			// a transport error can name internal hosts, so the client learns only which member failed
			clientErr = fmt.Errorf("%w: %s", ErrMemberUnavailable, name)
		}
		if qr.q.failed != nil {
			qr.q.failed(name, err)
		}
		// a limit or cancellation ends the query rather than shrinking its answer
		if !qr.q.partial || errors.Is(err, ErrFetchLimit) || ctx.Err() != nil {
			return storage.ErrSeriesSet(clientErr)
		}
		warnings.Add(clientErr)
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
