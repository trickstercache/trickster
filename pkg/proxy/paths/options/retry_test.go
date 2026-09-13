/*
 * Copyright 2026 The Trickster Authors
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

package options

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

func TestRetryOptions(t *testing.T) {
	var nilRetry *RetryOptions
	require.Nil(t, nilRetry.Clone())
	require.NoError(t, nilRetry.Validate())
	require.Nil(t, nilRetry.Budget())
	require.False(t, nilRetry.Retries(502))
	require.Equal(t, DefaultRetryBudgetPercent, nilRetry.ResolvedBudgetPercent())
	nilRetry.Initialize()
	require.True(t, nilRetry.Equal(nil))

	r := &RetryOptions{Attempts: 2, Codes: []int{502, 503}, Backoff: timeconv.Duration(time.Millisecond)}
	require.NoError(t, r.Validate())
	require.True(t, r.Retries(503))
	require.False(t, r.Retries(500))
	require.Equal(t, DefaultRetryBudgetPercent, r.ResolvedBudgetPercent())
	r.BudgetPercent = 50
	require.Equal(t, 50, r.ResolvedBudgetPercent())

	c := r.Clone()
	require.True(t, r.Equal(c))
	c.Codes[0] = 500
	require.False(t, r.Equal(c))
	require.False(t, r.Equal(nil))

	require.NotNil(t, r.Budget(), "the budget is created on demand")
	require.Same(t, r.Budget(), r.Budget())

	for _, tc := range []struct {
		name string
		r    RetryOptions
		err  error
	}{
		{"no attempts", RetryOptions{}, ErrInvalidRetryAttempts},
		{"too many attempts", RetryOptions{Attempts: MaxRetryAttempts + 1}, ErrInvalidRetryAttempts},
		{"bad code", RetryOptions{Attempts: 1, Codes: []int{99}}, ErrInvalidRetryCode},
		{"negative backoff", RetryOptions{Attempts: 1, Backoff: -1}, ErrInvalidRetryBackoff},
		{"bad budget", RetryOptions{Attempts: 1, BudgetPercent: 101}, ErrInvalidRetryBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.r.Validate(), tc.err)
		})
	}
}

func TestRetryBudget(t *testing.T) {
	var nilBudget *RetryBudget
	nilBudget.Request()
	require.True(t, nilBudget.Allow())

	b := NewRetryBudget(20)
	// the minimum is always available, then one retry per five requests
	for range retryBudgetMinimum {
		require.True(t, b.Allow())
	}
	require.False(t, b.Allow())
	for range 5 {
		b.Request()
	}
	require.False(t, b.Allow(), "3 retries against 5 requests exceeds 20 percent")
	for range 15 {
		b.Request()
	}
	require.True(t, b.Allow(), "3 retries against 20 requests is within 20 percent")
	require.False(t, b.Allow())

	// a new window starts over
	b.window.Load().startNano = time.Now().Add(-2 * retryBudgetWindow).UnixNano()
	require.True(t, b.Allow())
	require.EqualValues(t, 1, b.window.Load().retries.Load())
	require.EqualValues(t, 0, b.window.Load().requests.Load())
}

func TestRetryBudgetConcurrentAdmission(t *testing.T) {
	// callers racing for the last admissions cannot all be admitted, and a window that
	// rolls over under them loses no admission it granted
	b := NewRetryBudget(20)
	const callers = 64
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if b.Allow() {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, retryBudgetMinimum, admitted.Load())
	require.EqualValues(t, retryBudgetMinimum, b.window.Load().retries.Load())

	for range 100 {
		b.Request()
	}
	// 20 percent of 100 requests admits 20 retries in all, 3 already spent
	admitted.Store(0)
	for range callers {
		wg.Go(func() {
			if b.Allow() {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 17, admitted.Load())

	// rollover races: every caller lands in exactly one window
	b.window.Load().startNano = time.Now().Add(-2 * retryBudgetWindow).UnixNano()
	admitted.Store(0)
	for range callers {
		wg.Go(func() {
			if b.Allow() {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, retryBudgetMinimum, admitted.Load())
	require.EqualValues(t, retryBudgetMinimum, b.window.Load().retries.Load())
}

func TestIsIdempotent(t *testing.T) {
	for _, m := range []string{
		http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodTrace, http.MethodPut, http.MethodDelete,
	} {
		require.True(t, IsIdempotent(m), m)
	}
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodConnect, ""} {
		require.False(t, IsIdempotent(m), m)
	}
}

func TestMirrorOptions(t *testing.T) {
	var nilMirror *MirrorOptions
	require.Nil(t, nilMirror.Clone())
	require.ErrorIs(t, nilMirror.Validate(), ErrMirrorBackendRequired)
	require.Equal(t, DefaultMirrorPercent, nilMirror.ResolvedPercent())
	require.Equal(t, DefaultMirrorMaxInFlight, nilMirror.ResolvedMaxInFlight())
	require.True(t, nilMirror.Equal(nil))

	m := &MirrorOptions{BackendName: "shadow"}
	require.NoError(t, m.Validate())
	require.Equal(t, DefaultMirrorPercent, m.ResolvedPercent())
	require.Equal(t, DefaultMirrorMaxInFlight, m.ResolvedMaxInFlight())
	m.Percent, m.MaxInFlight = 25, 8
	require.Equal(t, 25, m.ResolvedPercent())
	require.Equal(t, 8, m.ResolvedMaxInFlight())
	c := m.Clone()
	require.True(t, m.Equal(c))
	c.Percent = 50
	require.False(t, m.Equal(c))
	require.False(t, m.Equal(nil))

	require.ErrorIs(t, (&MirrorOptions{}).Validate(), ErrMirrorBackendRequired)
	require.ErrorIs(t, (&MirrorOptions{BackendName: "s", Percent: 101}).Validate(), ErrInvalidMirrorPercent)
	require.ErrorIs(t, (&MirrorOptions{BackendName: "s", MaxInFlight: -1}).Validate(), ErrInvalidMirrorInFlight)
}

func TestUpstreamPolicyOptions(t *testing.T) {
	var nilOpts *Options
	require.False(t, nilOpts.HasUpstreamPolicy())
	o := New()
	require.False(t, o.HasUpstreamPolicy())
	o.Timeout = timeconv.Duration(time.Second)
	o.AttemptTimeout = timeconv.Duration(2 * time.Second)
	_, err := o.Validate()
	require.ErrorIs(t, err, ErrInvalidAttemptTimeout)
	o.AttemptTimeout = timeconv.Duration(500 * time.Millisecond)
	o.Retry = &RetryOptions{Attempts: 1}
	o.Mirrors = []*MirrorOptions{{BackendName: "shadow"}}
	ok, err := o.Validate()
	require.True(t, ok)
	require.NoError(t, err)
	require.True(t, o.HasUpstreamPolicy())
	require.NoError(t, o.Initialize(""))
	require.NotNil(t, o.Retry.budget)

	c := o.Clone()
	require.NotSame(t, o.Retry, c.Retry)
	require.NotSame(t, o.Mirrors[0], c.Mirrors[0])
	require.True(t, o.Retry.Equal(c.Retry))
	require.True(t, o.Mirrors[0].Equal(c.Mirrors[0]))
	require.Equal(t, o.Timeout, c.Timeout)

	o.Timeout = -1
	_, err = o.Validate()
	require.Error(t, err)
	o.Timeout = 0
	o.Retry = &RetryOptions{}
	_, err = o.Validate()
	require.ErrorIs(t, err, ErrInvalidRetryAttempts)
	o.Retry = nil
	o.Mirrors = []*MirrorOptions{{}}
	_, err = o.Validate()
	require.ErrorIs(t, err, ErrMirrorBackendRequired)
}

func TestRetryBudgetOfEverything(t *testing.T) {
	// a budget of every request is no budget: retries are never refused
	b := NewRetryBudget(100)
	for range retryBudgetMinimum * 4 {
		require.True(t, b.Allow())
	}
	r := &RetryOptions{Attempts: 2, BudgetPercent: 100}
	require.NoError(t, r.Validate())
	require.True(t, r.Budget().Allow())
}
