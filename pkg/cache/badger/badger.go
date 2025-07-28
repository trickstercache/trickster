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

// Package badger is the BadgerDB implementation of the Trickster Cache
package badger

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/dgraph-io/badger/v4"
)

var (
	// CacheClient implements the cache.Client interface
	_ cache.Client = &CacheClient{}
)

// CacheClient describes a Badger CacheClient
type CacheClient struct {
	Name   string
	Config *options.Options
	dbh    *badger.DB
}

func New(name string, cfg *options.Options) *CacheClient {
	c := &CacheClient{
		Name:   name,
		Config: cfg,
	}
	return c
}

// Connect opens the configured Badger key-value store
func (c *CacheClient) Connect() error {
	opts := badger.DefaultOptions(c.Config.Badger.Directory)
	opts.ValueDir = c.Config.Badger.ValueDirectory

	var err error
	c.dbh, err = badger.Open(opts)
	if err != nil {
		return err
	}

	return nil
}

func (c *CacheClient) Remove(cacheKeys ...string) error {
	return c.dbh.Update(func(txn *badger.Txn) error {
		for _, cacheKey := range cacheKeys {
			if err := txn.Delete([]byte(cacheKey)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *CacheClient) Close() error {
	return c.dbh.Close()
}

// Store places the the data into the Badger Cache using the provided Key and TTL
func (c *CacheClient) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return c.dbh.Update(func(txn *badger.Txn) error {
		return txn.SetEntry(&badger.Entry{Key: []byte(cacheKey), Value: data, ExpiresAt: uint64(time.Now().Add(ttl).Unix())}) // #nosec G115 - assume time values are positive
	})
}

// Retrieve gets data from the Badger Cache using the provided Key
// because Badger manages Object Expiration internally, allowExpired is not used.
func (c *CacheClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	var data []byte
	err := c.dbh.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(cacheKey))
		if err != nil {
			return err
		}
		data, err = item.ValueCopy(nil)
		return err

	})

	if err == nil {
		return data, status.LookupStatusHit, nil
	}

	if err == badger.ErrKeyNotFound {
		err = cache.ErrKNF
		return nil, status.LookupStatusKeyMiss, err
	}

	return data, status.LookupStatusError, err
}
