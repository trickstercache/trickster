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

// Package bbolt is the bbolt implementation of the Trickster Cache
package bbolt

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"go.etcd.io/bbolt"
)

// CacheClient implements the cache.Client interface
var _ cache.Client = &CacheClient{}

// CacheClient describes a BBolt CacheClient
type CacheClient struct {
	Name   string
	Config *options.Options
	dbh    *bbolt.DB
}

// New returns a new bbolt cache as a Trickster Cache Interface type
func New(cacheName, fileName, bucketName string, opts *options.Options) *CacheClient {
	if opts == nil {
		opts = options.New()
	}

	if bucketName != "" {
		opts.BBolt.Bucket = bucketName
	}

	if fileName != "" {
		opts.BBolt.Filename = fileName
	}

	c := &CacheClient{
		Name:   cacheName,
		Config: opts,
	}
	return c
}

func (c *CacheClient) Close() error {
	if c.dbh == nil {
		return nil
	}
	return c.dbh.Close()
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *CacheClient) Connect() error {
	var err error
	c.dbh, err = bbolt.Open(c.Config.BBolt.Filename, 0o644, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	err = c.dbh.Update(func(tx *bbolt.Tx) error {
		_, err2 := tx.CreateBucketIfNotExists([]byte(c.Config.BBolt.Bucket))
		if err2 != nil {
			return fmt.Errorf("create bucket: %w", err2)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (c *CacheClient) Store(cacheKey string, data []byte, _ time.Duration) error {
	err := writeToBBolt(c.dbh, c.Config.BBolt.Bucket, cacheKey, data)
	if err != nil {
		return err
	}
	return nil
}

func writeToBBolt(dbh *bbolt.DB, bucketName, cacheKey string, data []byte) error {
	err := dbh.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		err2 := b.Put([]byte(cacheKey), data)
		return err2
	})
	return err
}

func (c *CacheClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	var data []byte
	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		data = b.Get([]byte(cacheKey))
		if data == nil {
			return cache.ErrKNF
		}
		return nil
	})
	if err != nil {
		return nil, status.LookupStatusKeyMiss, err
	}

	return data, status.LookupStatusHit, nil
}

func (c *CacheClient) Remove(cacheKey ...string) error {
	err := c.dbh.Update(func(tx *bbolt.Tx) error {
		for _, cacheKey := range cacheKey {
			b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
			if err := b.Delete([]byte(cacheKey)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
