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
func WithConfigs(ctx context.Context, o *config.OriginConfig, c cache.Cache, p *config.PathConfig) context.Context {

	if o != nil {
		ctx = context.WithValue(ctx, originConfigKey, o)
	}
	if c != nil {
		ctx = context.WithValue(ctx, cacheConfigKey, c.Configuration())
	}
	if c != nil {
		ctx = context.WithValue(ctx, cacheClientKey, c)
	}
	if p != nil {
		ctx = context.WithValue(ctx, pathConfigKey, p)
	}
	return ctx
}

// OriginConfig returns the OriginConfig reference from the request context
func OriginConfig(ctx context.Context) *config.OriginConfig {
	i := ctx.Value(originConfigKey)
	if i != nil {
		return i.(*config.OriginConfig)
	}
	return nil
}

// CachingConfig returns the CachingConfig reference from the request context
func CachingConfig(ctx context.Context) *config.CachingConfig {
	i := ctx.Value(cacheConfigKey)
	if i != nil {
		return i.(*config.CachingConfig)
	}
	return nil
}

// PathConfig returns the PathConfig reference from the request context
func PathConfig(ctx context.Context) *config.PathConfig {
	i := ctx.Value(pathConfigKey)
	if i != nil {
		return i.(*config.PathConfig)
	}
	return nil
}

// CacheClient returns the Cache Client reference from the request context
func CacheClient(ctx context.Context) cache.Cache {
	i := ctx.Value(cacheClientKey)
	if i != nil {
		return i.(cache.Cache)
	}
	return nil
}
