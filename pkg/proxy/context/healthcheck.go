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
)

// WithHealthCheckFlag returns a copy of the provided context that also includes a bit
// indicating the request is performing a health check
func WithHealthCheckFlag(ctx context.Context, isHealthCheck bool) context.Context {
	return context.WithValue(ctx, healthCheckKey, isHealthCheck)
}

// HealthCheckFlag returns true if the request is a health check request
func HealthCheckFlag(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(healthCheckKey)
	if v != nil {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
