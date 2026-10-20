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
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const (
	// raceRounds repeats a race whose outcomes are ready together, so a wait choosing between them at random picks the
	// wrong one in some round but for one chance in 2^32
	raceRounds   = 32
	raceDeadline = time.Minute
	raceGapMs    = 1000
	// raceDeadlineName names a subtest by whether the select's context ends by its deadline
	raceDeadlineName = "/deadline="
	raceRoundFormat  = "round %d"
	// raceLeaveChecks bounds how often a test looks for a goroutine leaving a flight before it fails
	raceLeaveChecks = 100_000
)

// raceLeaving is in the stack of a goroutine leaving a flight
var raceLeaving = []byte(").leave(")

var errRaceLimit = fmt.Errorf("%w: response exceeds its byte limit", ErrFetchLimit)

// raceSelect is a select reading a member's gaps, each by joining a read another select started, which holds until finish
type raceSelect struct {
	rc  *rawTestCache
	src *chunkedSource
	k   rawKey
	// budget's charges of samples, each read's first payment as it joins, send their samples on entered and wait for release
	budget             *Budget
	entered            chan int64
	released, finished chan struct{}
	release, finish    func()
	ctx                context.Context
	cancel             context.CancelFunc
	deadline           bool
	// running counts the goroutines the select and the other select's reads run
	running sync.WaitGroup
}

// newRaceSelect returns a select whose budget allows maxSeries series, and whose context ends by its deadline when deadline
// is set, else by its cancellation; it must be made within a synctest bubble, whose clock lets the deadline pass
func newRaceSelect(maxSeries int64, deadline bool) *raceSelect {
	s := &raceSelect{
		rc:       newRawTestCache(&rawByteCache{}, RawCacheOptions{}),
		src:      &chunkedSource{ID: rawTestIdentity, err: errors.New("unshared read")},
		k:        rawKey{key: rawKeyPrefix + "race"},
		budget:   &Budget{MaxSeries: maxSeries},
		entered:  make(chan int64, rawGapReads),
		released: make(chan struct{}), finished: make(chan struct{}),
		deadline: deadline,
	}
	s.release = sync.OnceFunc(func() { close(s.released) })
	s.finish = sync.OnceFunc(func() { close(s.finished) })
	s.ctx, s.cancel = context.WithTimeout(context.Background(), raceDeadline)
	s.budget.share = func(_, samples int64, _ []uint64) error {
		if samples > 0 {
			s.entered <- samples
			<-s.released
		}
		return nil
	}
	return s
}

// stop ends everything the select and the reads it joined hold, and waits for their goroutines and stores
func (s *raceSelect) stop() {
	s.cancel()
	s.release()
	s.finish()
	s.running.Wait()
	s.rc.Wait()
}

// raceGap returns the ith gap a select reads
func raceGap(i int) timeseries.Extent {
	return extent(int64(i)*raceGapMs, int64(i)*raceGapMs+raceGapMs-1)
}

// share starts read as the shared read of gap for another select, which affords anything, returning a channel that closes
// once that select has the read's answer
func (s *raceSelect) share(gap timeseries.Extent, read func(context.Context, shareFunc) fetched) <-chan struct{} {
	answered := make(chan struct{})
	key := s.k.key + "/" + strconv.FormatInt(gap.Start.UnixMilli(), 10) + "/" + strconv.FormatInt(gap.End.UnixMilli(), 10)
	s.running.Go(func() {
		defer close(answered)
		s.rc.flights.do(context.Background(), key, &selectCharge{}, read, nil)
	})
	return answered
}

// holding returns a shared read that charges series and samples, closes charged, and holds until the select finishes,
// answering err, or until its context ends
func (s *raceSelect) holding(series, samples int64, charged chan struct{}, err error) func(context.Context, shareFunc) fetched {
	return func(ctx context.Context, share shareFunc) fetched {
		if e := share(series, samples, nil); e != nil {
			return fetched{err: e}
		}
		close(charged)
		select {
		case <-s.finished:
			return fetched{err: err}
		case <-ctx.Done():
			return fetched{err: ctx.Err()}
		}
	}
}

// fetch reads gaps through the select's charge, which counts series by the most any read held, returning a channel of the error
func (s *raceSelect) fetch(gaps ...timeseries.Extent) <-chan error {
	out := make(chan error, 1)
	charge := newSelectCharge(s.budget, false)
	s.running.Go(func() {
		_, err := s.rc.fetchAll(s.ctx, s.k, Member{Name: rawTestMember, Source: s.src}, gaps, span{}, heldExtents{}, nil,
			nil, charge)
		out <- err
	})
	return out
}

// end ends the select's context: cancels it, or with deadline set, waits while every other goroutine of the bubble is
// blocked, so its clock reaches the deadline
func (s *raceSelect) end() {
	if s.deadline {
		<-s.ctx.Done()
		return
	}
	s.cancel()
}

// contextEnd returns the error a context ended by its deadline, or else by cancellation, reports
func contextEnd(deadline bool) error {
	if deadline {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

func TestRawCacheFetchLimitOutranksContextEndAcrossGaps(t *testing.T) {
	for name, limit := range map[string]bool{"limit reached": true, "no limit reached": false} {
		for _, deadline := range []bool{false, true} {
			t.Run(name+raceDeadlineName+strconv.FormatBool(deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newRaceSelect(3, deadline)
					defer s.stop()
					// the second gap's read holds five series, more than the select allows, unless no limit is reached
					series := int64(5)
					if !limit {
						series = 1
					}
					charged := []chan struct{}{make(chan struct{}), make(chan struct{})}
					s.share(raceGap(0), s.holding(1, 1, charged[0], nil))
					s.share(raceGap(1), s.holding(series, 2, charged[1], nil))
					<-charged[0]
					<-charged[1]
					result := s.fetch(raceGap(0), raceGap(1))
					// both reads have joined, past the context's first check, and are paying for what was read before
					paid := map[int64]bool{<-s.entered: true}
					paid[<-s.entered] = true
					require.Equal(t, map[int64]bool{1: true, 2: true}, paid)
					s.end()
					s.release()
					err := <-result
					if !limit {
						require.ErrorIs(t, err, contextEnd(deadline))
						require.NotErrorIs(t, err, ErrFetchLimit)
						return
					}
					// the second read's payment fails on the limit, and the first then ends with the context, which the limit outranks
					require.ErrorIs(t, err, ErrFetchLimit)
					require.NotErrorIs(t, err, context.Canceled)
					require.NotErrorIs(t, err, context.DeadlineExceeded)
					s.stop()
					require.Empty(t, s.src.takeReads(), "each gap joined the other select's read")
				})
			})
		}
	}
}

func TestRawCacheOneGapReadOutcomeOutranksContextEnd(t *testing.T) {
	tests := map[string]struct {
		// finishes is whether the shared read finishes with a limit failure before the caller's context ends, and outgrows
		// whether it charges the caller more series than its budget allows by then
		finishes, outgrows bool
	}{
		"read finished with a limit failure": {finishes: true},
		"read outgrew the caller":            {outgrows: true},
		"read still under way":               {},
	}
	for name, tc := range tests {
		for _, deadline := range []bool{false, true} {
			t.Run(name+raceDeadlineName+strconv.FormatBool(deadline), func(t *testing.T) {
				for round := range raceRounds {
					synctest.Test(t, func(t *testing.T) {
						s := newRaceSelect(1, deadline)
						defer s.stop()
						gap := raceGap(round)
						charged, grow, grown, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}),
							make(chan struct{})
						answered := s.share(gap, func(ctx context.Context, share shareFunc) fetched {
							if err := share(1, 1, nil); err != nil {
								return fetched{err: err}
							}
							close(charged)
							if tc.outgrows {
								select {
								case <-grow:
								case <-s.finished:
									return fetched{}
								}
								// the late caller, paying for what the read held as it joined, can't afford two more series
								if err := share(2, 0, nil); err != nil {
									return fetched{err: err}
								}
								close(grown)
							}
							if tc.finishes {
								select {
								case <-finished:
									return fetched{err: errRaceLimit}
								case <-s.finished:
									return fetched{}
								}
							}
							<-s.finished
							return fetched{}
						})
						<-charged
						result := s.fetch(gap)
						require.Equal(t, int64(1), <-s.entered)
						switch {
						case tc.finishes:
							close(finished)
							<-answered
						case tc.outgrows:
							close(grow)
							<-grown
						}
						// the read's outcome and the context's end are both ready as the caller, done paying, waits
						s.end()
						s.release()
						err := <-result
						if tc.finishes || tc.outgrows {
							require.ErrorIs(t, err, ErrFetchLimit, raceRoundFormat, round)
							require.NotErrorIs(t, err, contextEnd(deadline), raceRoundFormat, round)
							return
						}
						require.ErrorIs(t, err, contextEnd(deadline))
					})
				}
			})
		}
	}
}

func TestRawCacheFailureAcrossGapsWithoutContextEnd(t *testing.T) {
	errMember := errors.New("member down")
	for name, tc := range map[string]struct {
		readErr error
		want    error
	}{
		// a failure other than a limit decides when the select's context hasn't ended
		"a member's failure": {readErr: errMember, want: errMember},
		// a read canceled on its own, the select's context going on, reports its cancellation
		"a read's own cancellation": {readErr: context.Canceled, want: context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRaceSelect(0, false)
				defer s.stop()
				// no other select shares these reads, so each reads the member, which fails
				s.src.err = tc.readErr
				err := <-s.fetch(raceGap(0), raceGap(1))
				require.ErrorIs(t, err, tc.want)
				require.NotErrorIs(t, err, ErrFetchLimit)
				require.NotEmpty(t, s.src.takeReads())
			})
		})
	}
}

// waitLeaving waits until a goroutine is leaving a flight, as one whose context ended does, though blocked on the flight's
// lock, which a wait on a channel can't detect
func waitLeaving(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for range raceLeaveChecks {
		if bytes.Contains(buf[:runtime.Stack(buf, true)], raceLeaving) {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("no goroutine left the flight")
}

func TestRawCacheChargeUnderWayOutranksContextEnd(t *testing.T) {
	for name, outgrows := range map[string]bool{"charge outgrows the caller": true, "charge within the caller's budget": false} {
		for _, deadline := range []bool{false, true} {
			t.Run(name+raceDeadlineName+strconv.FormatBool(deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newRaceSelect(1, deadline)
					defer s.stop()
					gap := raceGap(0)
					charged, grow := make(chan struct{}), make(chan struct{})
					// the read charges the select two more series, past its budget, unless the charge doesn't outgrow it
					series := int64(2)
					if !outgrows {
						series = 0
					}
					s.share(gap, func(ctx context.Context, share shareFunc) fetched {
						if err := share(1, 0, nil); err != nil {
							return fetched{err: err}
						}
						close(charged)
						<-grow
						if err := share(series, 1, nil); err != nil {
							return fetched{err: err}
						}
						select {
						case <-s.finished:
						case <-ctx.Done():
						}
						return fetched{}
					})
					<-charged
					result := s.fetch(gap)
					// once the select has joined and waits, the read charges it, holding the flight's lock as its budget takes the sample
					synctest.Wait()
					close(grow)
					require.Equal(t, int64(1), <-s.entered)
					// the select's context ends, and as it leaves the flight, it waits on the charge under way, which then completes
					s.end()
					waitLeaving(t)
					s.release()
					err := <-result
					if outgrows {
						require.ErrorIs(t, err, ErrFetchLimit)
						require.NotErrorIs(t, err, contextEnd(deadline))
						return
					}
					require.ErrorIs(t, err, contextEnd(deadline))
				})
			})
		}
	}
}

func TestRawCacheFetchAllWithoutGapsAllocatesNothing(t *testing.T) {
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), raceDeadline)
	defer cancel()
	m := Member{Name: rawTestMember, Source: &chunkedSource{ID: rawTestIdentity}}
	charge := newSelectCharge(nil, false)
	var results []fetched
	var err error
	// a select the cache answered whole reads nothing, so it neither derives a context nor starts a read
	allocs := testing.AllocsPerRun(100, func() {
		results, err = rc.fetchAll(ctx, rawKey{key: rawKeyPrefix}, m, nil, span{}, heldExtents{}, nil, nil, charge)
	})
	require.Zero(t, allocs)
	require.NoError(t, err)
	require.Empty(t, results)
	cancel()
	_, err = rc.fetchAll(ctx, rawKey{key: rawKeyPrefix}, m, nil, span{}, heldExtents{}, nil, nil, charge)
	require.ErrorIs(t, err, context.Canceled, "a select whose context ended reports it, cached or not")
}
