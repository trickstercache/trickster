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

// Package redis is the implementation of the redis and valkey Trickster Cache providers,
// and supports Standalone, Sentinel and Cluster
package redis

import (
	"context"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachemetrics "github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	valkeyopts "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

// CacheClient implements the cache.Client interface
var _ cache.Client = &CacheClient{}

// Redis is the string "redis"
const Redis = "redis"

const (
	serverInfoSection = "server"
	valkeyServerName  = "server_name:valkey"
)

// CacheClient represents a redis or valkey cache client that conforms to the cache.Client interface
type CacheClient struct {
	Name   string
	Config *options.Options
	ctx    context.Context
	holder *Holder
	// clientCache reads through valkey-go's client-side cache, keeping entries up to clientCacheTTL
	clientCache    bool
	clientCacheTTL time.Duration
	localHits      prometheus.Counter
}

func New(ctx context.Context, name string, cfg *options.Options) *CacheClient {
	return &CacheClient{
		Name:   name,
		Config: cfg,
		ctx:    ctx,
	}
}

// connection returns the cache's connection options and, for a valkey cache, its Valkey-only
// settings; a nil Settings is the redis provider's classic profile
func (c *CacheClient) connection() (*redisopts.Options, *valkeyopts.Settings) {
	if v := c.Config.Valkey; v != nil && (c.Config.ProviderID == providers.ValkeyID || c.Config.Redis == nil) {
		return &v.Options, &v.Settings
	}
	return c.Config.Redis, nil
}

// Connect connects to the configured endpoint. An unreachable server is not fatal: the client
// connects once the server is up, and commands fail as errors until then.
func (c *CacheClient) Connect() error {
	ro, s := c.connection()
	opt, err := clientOption(ro, s)
	if err != nil {
		return err
	}
	c.holder.close()
	c.clientCache = s != nil && s.ClientSideCache.Enabled
	if c.clientCache {
		c.clientCacheTTL = time.Duration(s.ClientSideCache.MaxTTL)
		c.localHits = cachemetrics.ClientCacheHits(c.Name, c.Config.Provider)
	}
	c.holder = newHolder(opt,
		positiveOr(time.Duration(ro.MinRetryBackoff), defaultMinRetryBackoff),
		positiveOr(time.Duration(ro.MaxRetryBackoff), defaultMaxRetryBackoff),
		func(cl valkey.Client) { c.checkServer(cl, s != nil) })
	return c.holder.connect()
}

// Holder returns the connection's client holder, or nil before Connect builds it
func (c *CacheClient) Holder() *Holder {
	return c.holder
}

// checkServer logs when a valkey cache's server isn't Valkey, as its Valkey-only settings may
// then fail, and when a redis cache's server is Valkey, which the valkey provider serves better
func (c *CacheClient) checkServer(cl valkey.Client, isValkeyCache bool) {
	ctx, cancel := context.WithTimeout(c.ctx, connTimeout(c.connectionOptions()))
	defer cancel()
	info, err := cl.Do(ctx, cl.B().Info().Section(serverInfoSection).Build()).ToString()
	if err != nil {
		return
	}
	isValkey := strings.Contains(info, valkeyServerName)
	switch {
	case isValkeyCache && !isValkey:
		logger.Warn("valkey cache is connected to a server that is not Valkey; "+
			"its Valkey-only settings may not work", logging.Pairs{keys.CacheName: c.Name})
	case !isValkeyCache && isValkey:
		logger.Info("redis cache is connected to a Valkey server; "+
			"the valkey provider adds Valkey-only settings", logging.Pairs{keys.CacheName: c.Name})
	}
}

func (c *CacheClient) connectionOptions() *redisopts.Options {
	ro, _ := c.connection()
	return ro
}

// Remove deletes the keys, unlinking them so the server frees their memory in the background
func (c *CacheClient) Remove(cacheKeys ...string) error {
	cl, err := c.holder.Client()
	if err != nil {
		return err
	}
	if len(cacheKeys) == 1 {
		return cl.Do(c.ctx, cl.B().Unlink().Key(cacheKeys[0]).Build()).Error()
	}
	// a cluster client unlinks each key on its own, as one command's keys must share a slot
	for _, err := range valkey.MUnlink(cl, c.ctx, cacheKeys) {
		if err != nil {
			return err
		}
	}
	return nil
}

// Store places the data into the cache using the provided Key and TTL; a ttl of 0 never expires
func (c *CacheClient) Store(cacheKey string, data []byte, ttl time.Duration) error {
	cl, err := c.holder.Client()
	if err != nil {
		return err
	}
	// BinaryString views data without a copy, which is safe as Do returns only after the write
	v := cl.B().Set().Key(cacheKey).Value(valkey.BinaryString(data))
	if ttl > 0 {
		return cl.Do(c.ctx, v.PxMilliseconds(max(ttl.Milliseconds(), 1)).Build()).Error()
	}
	return cl.Do(c.ctx, v.Build()).Error()
}

// Retrieve gets data from the cache using the provided Key. The server manages object
// expiration, so allowExpired is not used. The returned bytes may be shared with other callers
// and with the client-side cache, and must not be modified.
func (c *CacheClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	cl, err := c.holder.Client()
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	var r valkey.ValkeyResult
	if c.clientCache {
		r = cl.DoCache(c.ctx, cl.B().Get().Key(cacheKey).Cache(), c.clientCacheTTL)
	} else {
		r = cl.Do(c.ctx, cl.B().Get().Key(cacheKey).Build())
	}
	b, err := r.AsBytes()
	if err == nil {
		if c.clientCache && r.IsCacheHit() {
			c.localHits.Inc()
		}
		return b, status.LookupStatusHit, nil
	}
	if valkey.IsValkeyNil(err) {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return nil, status.LookupStatusError, err
}

func (c *CacheClient) Close() error {
	c.holder.close()
	return nil
}
