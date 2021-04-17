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

// WithRequestBody returns a copy of the provided context that also includes
// the provided request body reference
func WithRequestBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, requestBodyKey, body)
}

// RequestBody returns the request body associated with the request
func RequestBody(ctx context.Context) []byte {
	v := ctx.Value(requestBodyKey)
	if v != nil {
		if b, ok := v.([]byte); ok {
			return b
		}
	}
	return nil
}
