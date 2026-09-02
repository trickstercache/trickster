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

package context

import (
	"context"
	"net/http"
)

// WithServed marks ctx as belonging to a request being served to a downstream
// client, which net/http indicates with its own key but HTTP/3 does not.
func WithServed(ctx context.Context) context.Context {
	return context.WithValue(ctx, servedKey, true)
}

// IsServed reports whether ctx belongs to a request served to a downstream
// client, over either a net/http listener or an HTTP/3 one. It gates behavior
// that only makes sense when a client connection can be broken, such as
// aborting a response whose body ended early.
func IsServed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if ctx.Value(http.ServerContextKey) != nil {
		return true
	}
	v, ok := ctx.Value(servedKey).(bool)
	return ok && v
}
