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

package acme

import (
	"context"
	"errors"
	"fmt"

	cacheopts "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/redis"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/redisstorage"

	"github.com/caddyserver/certmagic"
)

const redisClientName = "acme-storage"

var errNoRedisConnection = errors.New("acme redis storage has no connection settings")

// storageConnection returns the cache options that redis or valkey storage connects with: those
// of the cache it borrows, or its own. A valkey connection keeps no client-side cache and reads
// only from primaries, as a lock must see the latest write.
func storageConnection(conf *config.Config) *cacheopts.Options {
	s := conf.ACME.Storage
	if s == nil {
		return nil
	}
	name := s.CacheName()
	borrowed := conf.Caches[name]
	switch {
	case s.Provider == acmeopts.StorageRedis && s.Redis != nil:
		conn := s.Redis.Connection
		if name != "" {
			if borrowed == nil {
				return nil
			}
			conn = borrowed.Redis
		}
		if conn == nil {
			return nil
		}
		return &cacheopts.Options{Provider: providers.Redis, ProviderID: providers.RedisID, Redis: conn.Clone()}
	case s.Provider == acmeopts.StorageValkey && s.Valkey != nil:
		conn := s.Valkey.Connection
		if name != "" {
			if borrowed == nil {
				return nil
			}
			conn = borrowed.Valkey
		}
		if conn == nil {
			return nil
		}
		return &cacheopts.Options{
			Provider: providers.Valkey, ProviderID: providers.ValkeyID,
			Valkey: conn.WithoutReadFeatures(),
		}
	}
	return nil
}

// sameConnection reports whether two storage connections are identical, either being nil
func sameConnection(a, b *cacheopts.Options) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(b)
}

func newStorage(o *acmeopts.StorageOptions, conn *cacheopts.Options,
) (certmagic.Storage, func() error, error) {
	if o == nil {
		return nil, nil, acmeopts.ErrStoragePathRequired
	}
	if o.Provider != acmeopts.StorageRedis && o.Provider != acmeopts.StorageValkey {
		return &certmagic.FileStorage{Path: o.Path}, func() error { return nil }, nil
	}
	if conn == nil {
		return nil, nil, errNoRedisConnection
	}
	rc := redis.New(context.Background(), redisClientName, conn)
	if err := rc.Connect(); err != nil {
		if rc.Holder() == nil {
			return nil, nil, fmt.Errorf("acme redis storage: %w", err)
		}
		// the client connects once the server is up, so an unreachable server is retried rather than fatal
		logger.Warn("acme redis storage is not reachable yet", logging.Pairs{"detail": err.Error()})
	}
	s := redisstorage.New(rc.Holder(), o.KeyPrefix(), o.LockTTL(), o.Provider == acmeopts.StorageValkey)
	return s, func() error {
		err := s.Close()
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		return err
	}, nil
}
