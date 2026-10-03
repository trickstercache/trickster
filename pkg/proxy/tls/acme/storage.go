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
	"time"

	cacheopts "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/redis"
	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/redisstorage"

	"github.com/caddyserver/certmagic"
)

const redisClientName = "acme-storage"

var errNoRedisConnection = errors.New("acme redis storage has no connection settings")

func redisConnection(conf *config.Config) *redisopts.Options {
	s := conf.ACME.Storage
	if s == nil || s.Provider != acmeopts.StorageRedis || s.Redis == nil {
		return nil
	}
	if s.Redis.CacheName != "" {
		if c := conf.Caches[s.Redis.CacheName]; c != nil {
			return c.Redis
		}
		return nil
	}
	return s.Redis.Connection
}

func newStorage(o *acmeopts.StorageOptions, conn *redisopts.Options,
) (certmagic.Storage, func() error, error) {
	if o == nil {
		return nil, nil, acmeopts.ErrStoragePathRequired
	}
	if o.Provider != acmeopts.StorageRedis {
		return &certmagic.FileStorage{Path: o.Path}, func() error { return nil }, nil
	}
	if conn == nil {
		return nil, nil, errNoRedisConnection
	}
	rc := redis.New(context.Background(), redisClientName, &cacheopts.Options{Redis: conn})
	if err := rc.Connect(); err != nil {
		if rc.Cmdable() == nil {
			return nil, nil, fmt.Errorf("acme redis storage: %w", err)
		}
		// the client reconnects on its own, so an unreachable server is retried rather than fatal
		logger.Warn("acme redis storage is not reachable yet", logging.Pairs{"detail": err.Error()})
	}
	s := redisstorage.New(rc.Cmdable(), o.Redis.KeyPrefix, time.Duration(o.Redis.LockTTL))
	return s, func() error {
		err := s.Close()
		if cerr := rc.Close(); err == nil {
			err = cerr
		}
		return err
	}, nil
}
