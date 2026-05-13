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

package options

import (
	"runtime"
	"testing"
)

func ptr[T any](v T) *T {
	p := new(T)
	*p = v
	return p
}

// GetQueryConcurrencyLimit must clamp negative inputs to a non-negative value
// before they are handed to errgroup.SetLimit. A negative value silently
// disables the limit; a value of 0 here is treated by callers as "unlimited"
// via the `if limit > 0` gate.
func TestQueryConcurrencyLimitClampsNegatives(t *testing.T) {
	cases := []struct {
		name string
		opts *ConcurrencyOptions
	}{
		{name: "negative limit -1", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(-1)}},
		{name: "negative limit -100", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(-100)}},
		{name: "negative multiplier", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(4), QueryConcurrencyMultiplier: ptr(-2)}},
		{name: "negative limit and multiplier", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(-4), QueryConcurrencyMultiplier: ptr(-2)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.GetQueryConcurrencyLimit()
			if got < 0 {
				t.Errorf("GetQueryConcurrencyLimit returned %d; must be >= 0", got)
			}
		})
	}
}

func TestQueryConcurrencyLimitPositive(t *testing.T) {
	cases := []struct {
		name string
		opts *ConcurrencyOptions
		want int
	}{
		{name: "limit 1", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(1)}, want: 1},
		{name: "limit 10", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(10)}, want: 10},
		{name: "limit 4 multiplier 3", opts: &ConcurrencyOptions{QueryConcurrencyLimit: ptr(4), QueryConcurrencyMultiplier: ptr(3)}, want: 12},
		{name: "nil opts defaults to GOMAXPROCS", opts: nil, want: runtime.GOMAXPROCS(0)},
		{name: "default (nil pointer) is GOMAXPROCS", opts: &ConcurrencyOptions{}, want: runtime.GOMAXPROCS(0)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.GetQueryConcurrencyLimit()
			if got != tc.want {
				t.Errorf("GetQueryConcurrencyLimit: got %d, want %d", got, tc.want)
			}
		})
	}
}
