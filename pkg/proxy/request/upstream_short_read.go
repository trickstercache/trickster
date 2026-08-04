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

package request

import (
	"context"
	"sync/atomic"
)

// UpstreamShortReadCapture is a context sidecar that the proxy engine
// trips when an upstream's body ended before its declared Content-Length.
// ALB fanout binds one per shard and reads Tripped() to disqualify the
// slot; non-ALB callers leave it unbound and the proxy path is a no-op.
type UpstreamShortReadCapture struct {
	tripped atomic.Bool
}

// Mark flags a short read. Safe for concurrent use.
func (c *UpstreamShortReadCapture) Mark() { c.tripped.Store(true) }

// Tripped reports whether Mark was called.
func (c *UpstreamShortReadCapture) Tripped() bool { return c.tripped.Load() }

type shortReadKey struct{}

// WithUpstreamShortReadCapture binds a fresh capture to ctx.
func WithUpstreamShortReadCapture(ctx context.Context) (context.Context, *UpstreamShortReadCapture) {
	c := &UpstreamShortReadCapture{}
	return context.WithValue(ctx, shortReadKey{}, c), c
}

// GetUpstreamShortReadCapture returns the capture bound to ctx, or nil.
func GetUpstreamShortReadCapture(ctx context.Context) *UpstreamShortReadCapture {
	c, _ := ctx.Value(shortReadKey{}).(*UpstreamShortReadCapture)
	return c
}

// RebindUpstreamShortReadCapture carries the capture (if any) from src
// onto dst. Needed when callers rebuild the context from
// context.Background() and would otherwise drop the binding.
func RebindUpstreamShortReadCapture(dst, src context.Context) context.Context {
	if c := GetUpstreamShortReadCapture(src); c != nil {
		return context.WithValue(dst, shortReadKey{}, c)
	}
	return dst
}
