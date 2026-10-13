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
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	stopTestHeld     = "held"
	stopTestFailing  = "failing"
	stopTestDeadline = time.Minute
)

var (
	errStopTestLimit = fmt.Errorf("%w: more than 1 series", ErrFetchLimit)
	// errStopTestDown names a host, as a transport error can
	errStopTestDown = errors.New("dial tcp 10.0.0.1:9090: connection refused")
)

// heldSource holds each read until its context ends, then fails it with err, or else with its context's end
type heldSource struct {
	SourceIdentity
	err            error
	started, ended atomic.Int32
}

func (*heldSource) SeriesAPI() string { return prop.SeriesAPIRemoteRead }

func (s *heldSource) Select(ctx context.Context, _, _ int64, _ *storage.SelectHints, _ ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.started.Add(1)
	<-ctx.Done()
	s.ended.Add(1)
	if s.err != nil {
		return nil, nil, s.err
	}
	return nil, nil, ctx.Err()
}

// stopTestQueryable returns a queryable over held and failing members, held first when heldFirst is set, reading through
// a raw cache when cached is set, which it returns, and the names failed has learned of so far
func stopTestQueryable(held, failing Source, heldFirst, partial, cached bool) (*Queryable, *rawTestCache, *[]string) {
	members := []Member{{Name: stopTestFailing, Source: failing}, {Name: stopTestHeld, Source: held}}
	if heldFirst {
		slices.Reverse(members)
	}
	var failed []string
	q := NewQueryable(members, nil, merge.TSMDeduplicationChain, partial, func(member string, _ error) {
		failed = append(failed, member)
	})
	var rc *rawTestCache
	if cached {
		rc = newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		q.WithRawCache(rc.RawCache)
	}
	return q, rc, &failed
}

func TestQueryableDecidingFailureStopsOtherReads(t *testing.T) {
	tests := map[string]struct {
		err     error
		partial bool
		// stops is whether the failing member's failure stops the held member's read, which else holds until the deadline
		stops bool
		want  error
	}{
		"a limit":                             {err: errStopTestLimit, partial: true, stops: true, want: ErrFetchLimit},
		"a limit without partial responses":   {err: errStopTestLimit, stops: true, want: ErrFetchLimit},
		"a failure without partial responses": {err: errStopTestDown, stops: true, want: ErrMemberUnavailable},
		"a failure in a partial response":     {err: errStopTestDown, partial: true, want: context.DeadlineExceeded},
	}
	for name, tc := range tests {
		for _, cached := range []bool{false, true} {
			for _, heldFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/cached=%t/held first=%t", name, cached, heldFirst), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						held := &heldSource{ID: stopTestHeld}
						failing := &MemorySource{ID: stopTestFailing, Err: tc.err}
						q, rc, failed := stopTestQueryable(held, failing, heldFirst, tc.partial, cached)
						ctx, cancel := context.WithTimeout(context.Background(), stopTestDeadline)
						defer cancel()
						start := time.Now()
						set := selectAll(t, q, ctx, nil)
						elapsed := time.Since(start)
						if rc != nil {
							rc.Wait()
						}
						// a read the failure stopped before it began, as a shared read may be, never starts
						require.Equal(t, held.started.Load(), held.ended.Load())
						require.False(t, set.Next())
						require.ErrorIs(t, set.Err(), tc.want)
						require.NotErrorIs(t, set.Err(), errStopTestDown, "the client doesn't see the transport error")
						if !tc.stops {
							// a failure a partial response can leave out stops nothing, so the held read ends by the deadline
							require.Equal(t, stopTestDeadline, elapsed)
							require.Equal(t, int32(1), held.ended.Load())
							return
						}
						require.Zero(t, elapsed, "the held read stops as soon as the failure decides the query")
						require.ErrorContains(t, set.Err(), stopTestFailing)
						require.Equal(t, []string{stopTestFailing}, *failed, "the read stopped is no failure of its member")
					})
				})
			}
		}
	}
}

func TestQueryableLimitOutranksEarlierContextEnd(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		for _, limitFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("deadline=%t/limit first=%t", deadline, limitFirst), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					// both reads hold until the context ends, when one fails on a limit and the other with the context's end
					ended := &heldSource{ID: stopTestHeld}
					limited := &heldSource{ID: stopTestFailing, err: errStopTestLimit}
					q, _, failed := stopTestQueryable(ended, limited, !limitFirst, true, false)
					ctx, cancel := context.WithTimeout(context.Background(), stopTestDeadline)
					defer cancel()
					if !deadline {
						cancel()
					}
					set := selectAll(t, q, ctx, nil)
					require.False(t, set.Next())
					require.ErrorIs(t, set.Err(), ErrFetchLimit)
					require.ErrorContains(t, set.Err(), stopTestFailing)
					require.Contains(t, *failed, stopTestFailing, "the limited member's failure is reported")
				})
			})
		}
	}
}

func TestQueryableLimitOfStoppedReadDecides(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// without partial responses, the failing member stops the other's read, which reports a limit as it stops
		limited := &heldSource{ID: stopTestHeld, err: errStopTestLimit}
		failing := &MemorySource{ID: stopTestFailing, Err: errStopTestDown}
		q, _, failed := stopTestQueryable(limited, failing, true, false, false)
		set := selectAll(t, q, context.Background(), nil)
		require.False(t, set.Next())
		require.ErrorIs(t, set.Err(), ErrFetchLimit)
		require.ErrorContains(t, set.Err(), stopTestHeld)
		require.Contains(t, *failed, stopTestHeld)
	})
}
