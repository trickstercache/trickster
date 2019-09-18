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

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
)

// WithConfigs returns a copy of the provided context that also includes the OriginConfig, CachingConfig and PathConfig for the request
func WithConfigs(ctx context.Context, o *config.OriginConfig, c cache.Cache, p *config.ProxyPathConfig) context.Context {
	ctx = context.WithValue(ctx, originConfigKey, o)
	ctx = context.WithValue(ctx, cacheConfigKey, c.Configuration())
	ctx = context.WithValue(ctx, cacheClientKey, c)
	ctx = context.WithValue(ctx, pathConfigKey, p)
	return ctx
}

// OriginConfig returns the OriginConfig reference from the request context
func OriginConfig(ctx context.Context) *config.OriginConfig {
	return ctx.Value(originConfigKey).(*config.OriginConfig)
}

// CachingConfig returns the CachingConfig reference from the request context
func CachingConfig(ctx context.Context) *config.CachingConfig {
	return ctx.Value(cacheConfigKey).(*config.CachingConfig)
}

// PathConfig returns the PathConfig reference from the request context
func PathConfig(ctx context.Context) *config.ProxyPathConfig {
	return ctx.Value(pathConfigKey).(*config.ProxyPathConfig)
}

// CacheClient returns the Cache Client reference from the request context
func CacheClient(ctx context.Context) cache.Cache {
	return ctx.Value(cacheClientKey).(cache.Cache)
}
