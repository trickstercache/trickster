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

package context

import (
	"context"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
)

// WithHops returns a copy of the provided context that also includes
// rule-based Hop information about the request
func WithHops(ctx context.Context, current, max int) context.Context {
	return context.WithValue(ctx, hopsKey, []int{current, max})
}

// Hops returns the Hops data associated with the request
func Hops(ctx context.Context) (current, max int) {
	v := ctx.Value(hopsKey)
	if v != nil {
		if i, ok := v.([]int); ok && len(i) == 2 {
			return i[0], i[1]
		}
	}
	return 0, options.DefaultMaxRuleExecutions
}

// IncrementedRewriterHops returns the current incremented hop count from the ctx
func IncrementedRewriterHops(ctx context.Context, i int) int {
	v := ctx.Value(rewriterHopsKey)
	var p *int32
	if v != nil {
		if j, ok := v.(*int32); ok {
			p = j
		}
	}
	if p == nil {
		return 0
	}
	i = int(atomic.AddInt32(p, int32(i)))
	return i
}

// RewriterHops returns the RewriterHops data associated with the request
func RewriterHops(ctx context.Context) int {
	v := ctx.Value(rewriterHopsKey)
	if v != nil {
		if i, ok := v.(*int32); ok {
			return int(atomic.LoadInt32(i))
		}
	}
	return 0
}

// StartRewriterHops returns a context with rewriterHopsKey = 0
func StartRewriterHops(ctx context.Context) context.Context {
	var i int32
	return context.WithValue(ctx, rewriterHopsKey, &i)
}
