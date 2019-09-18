/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package context

import (
	"context"

	"github.com/Comcast/trickster/internal/config"
)

// WithConfigs returns a copy of the provided context that also includes the OriginConfig, CachingConfig and PathConfig for the request
func WithConfigs(ctx context.Context, o *config.OriginConfig, c *config.CachingConfig, p *config.ProxyPathConfig) context.Context {
	ctx = context.WithValue(ctx, originKey, o)
	ctx = context.WithValue(ctx, cacheKey, c)
	ctx = context.WithValue(ctx, pathKey, p)
	return ctx
}

// OriginConfig returns the OriginConfig reference from the request context
func OriginConfig(ctx context.Context) *config.OriginConfig {
	return ctx.Value(originKey).(*config.OriginConfig)
}

// CachingConfig returns the CachingConfig reference from the request context
func CachingConfig(ctx context.Context) *config.CachingConfig {
	return ctx.Value(cacheKey).(*config.CachingConfig)
}

// PathConfig returns the PathConfig reference from the request context
func PathConfig(ctx context.Context) *config.ProxyPathConfig {
	return ctx.Value(pathKey).(*config.ProxyPathConfig)
}
