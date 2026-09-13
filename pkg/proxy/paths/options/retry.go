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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultRetryBudgetPercent is the share of recent requests that may be retries.
	DefaultRetryBudgetPercent = 20
	// MaxRetryAttempts bounds how many extra upstream attempts a path may configure.
	MaxRetryAttempts = 10
	// retryBudgetWindow is how often the budget's request and retry counts start over.
	retryBudgetWindow = 10 * time.Second
	// retryBudgetMinimum is the number of retries always allowed per window, so a
	// path with little traffic is not denied every retry by a percentage of nothing.
	retryBudgetMinimum = 3
)

var (
	// ErrInvalidRetryAttempts is returned when retry attempts are out of range.
	ErrInvalidRetryAttempts = fmt.Errorf("retry attempts must be between 1 and %d", MaxRetryAttempts)
	// ErrInvalidRetryCode is returned when a retry status code is not a valid HTTP status.
	ErrInvalidRetryCode = errors.New("retry codes must be valid HTTP status codes (100-599)")
	// ErrInvalidRetryBackoff is returned for a negative backoff.
	ErrInvalidRetryBackoff = errors.New("retry backoff must not be negative")
	// ErrInvalidRetryBudget is returned when the budget percentage is out of range.
	ErrInvalidRetryBudget = errors.New("retry budget_percent must be between 1 and 100")
	// ErrInvalidAttemptTimeout is returned when attempt_timeout exceeds the path timeout.
	ErrInvalidAttemptTimeout = errors.New("attempt_timeout must not exceed timeout")
)

// RetryOptions configures bounded retries of idempotent upstream requests on a path.
type RetryOptions struct {
	// Attempts is the number of additional upstream attempts after the first fails
	Attempts int `yaml:"attempts,omitempty"`
	// Codes lists the upstream status codes that are retried; a connection failure
	// is always retried
	Codes []int `yaml:"codes,omitempty"`
	// Backoff is the wait before each retry
	Backoff timeconv.Duration `yaml:"backoff,omitempty"`
	// BudgetPercent bounds retries to this share of the path's requests over a sliding window;
	// 0 is the default budget and 100 removes the budget so every eligible request is retried
	BudgetPercent int `yaml:"budget_percent,omitempty"`

	budget *RetryBudget
}

// Clone returns a copy of the retry options sharing the path's live budget.
func (r *RetryOptions) Clone() *RetryOptions {
	if r == nil {
		return nil
	}
	out := *r
	out.Codes = slices.Clone(r.Codes)
	return &out
}

// Initialize creates the path's retry budget.
func (r *RetryOptions) Initialize() {
	if r == nil {
		return
	}
	r.budget = NewRetryBudget(r.ResolvedBudgetPercent())
}

// Validate checks the retry settings.
func (r *RetryOptions) Validate() error {
	if r == nil {
		return nil
	}
	if r.Attempts < 1 || r.Attempts > MaxRetryAttempts {
		return ErrInvalidRetryAttempts
	}
	for _, c := range r.Codes {
		if c < 100 || c > 599 {
			return fmt.Errorf("%w: %d", ErrInvalidRetryCode, c)
		}
	}
	if r.Backoff < 0 {
		return ErrInvalidRetryBackoff
	}
	if r.BudgetPercent < 0 || r.BudgetPercent > 100 {
		return ErrInvalidRetryBudget
	}
	return nil
}

// ResolvedBudgetPercent returns the configured budget, defaulted when unset.
func (r *RetryOptions) ResolvedBudgetPercent() int {
	if r == nil || r.BudgetPercent == 0 {
		return DefaultRetryBudgetPercent
	}
	return r.BudgetPercent
}

// Retries reports whether an upstream status code is one the path retries.
func (r *RetryOptions) Retries(status int) bool {
	return r != nil && slices.Contains(r.Codes, status)
}

// Budget returns the path's retry budget, creating one if the options were not initialized.
func (r *RetryOptions) Budget() *RetryBudget {
	if r == nil {
		return nil
	}
	if r.budget == nil {
		r.Initialize()
	}
	return r.budget
}

// Equal reports whether both retry configurations are the same.
func (r *RetryOptions) Equal(o *RetryOptions) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.Attempts == o.Attempts && r.Backoff == o.Backoff &&
		r.BudgetPercent == o.BudgetPercent && slices.Equal(r.Codes, o.Codes)
}

// RetryBudget bounds retries to a share of the requests seen in the current window.
type RetryBudget struct {
	percent int64
	window  atomic.Pointer[retryWindow]
}

// retryWindow counts one window's requests and admitted retries; a window is
// replaced whole when it elapses, so a count is never reset under a caller
type retryWindow struct {
	startNano int64
	requests  atomic.Int64
	retries   atomic.Int64
}

// NewRetryBudget returns a budget allowing retries up to percent of recent requests.
func NewRetryBudget(percent int) *RetryBudget {
	b := &RetryBudget{percent: int64(percent)}
	b.window.Store(&retryWindow{startNano: time.Now().UnixNano()})
	return b
}

// Request counts a request that could be retried.
func (b *RetryBudget) Request() {
	if b == nil {
		return
	}
	b.current().requests.Add(1)
}

// Allow reports whether the budget admits one more retry, and reserves it, so
// concurrent callers at the bound cannot all be admitted.
func (b *RetryBudget) Allow() bool {
	if b == nil || b.percent >= 100 {
		return true
	}
	w := b.current()
	for {
		retries := w.retries.Load()
		if retries >= retryBudgetMinimum && retries*100 >= w.requests.Load()*b.percent {
			return false
		}
		if w.retries.CompareAndSwap(retries, retries+1) {
			return true
		}
	}
}

func (b *RetryBudget) current() *retryWindow {
	// whichever caller swaps in the new window wins; the others count against it
	w := b.window.Load()
	nowNano := time.Now().UnixNano()
	if nowNano-w.startNano <= int64(retryBudgetWindow) {
		return w
	}
	fresh := &retryWindow{startNano: nowNano}
	if b.window.CompareAndSwap(w, fresh) {
		return fresh
	}
	return b.window.Load()
}

// idempotentMethods are the methods a retry may repeat without changing the outcome.
var idempotentMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
	http.MethodTrace: true, http.MethodPut: true, http.MethodDelete: true,
}

// IsIdempotent reports whether a request method may safely be retried.
func IsIdempotent(method string) bool {
	return idempotentMethods[method]
}
