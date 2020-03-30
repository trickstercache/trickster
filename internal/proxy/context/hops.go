/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/Comcast/trickster/internal/config/defaults"
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
	return 0, defaults.DefaultMaxInternalRedirects
}
