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

package main

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	bolt "github.com/coreos/bbolt"
	"github.com/go-kit/kit/log/level"
)

// BoltDBCache describes a BoltDB Cache
type BoltDBCache struct {
	T      *TricksterHandler
	Config BoltDBCacheConfig
	dbh    *bolt.DB
}

// Connect instantiates the BoltDBCache mutex map and starts the Expired Entry Reaper goroutine
func (c *BoltDBCache) Connect() error {

	fullPath := path.Join(c.Config.CachePath, c.Config.Filename)

	level.Info(c.T.Logger).Log("event", "boltdb cache setup", "cachePath", fullPath)

	err := makeDirectory(c.Config.CachePath)
	if err != nil {
		return err
	}

	c.dbh, err = bolt.Open(fullPath, 0644, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	err = c.dbh.Update(func(tx *bolt.Tx) error {
		tx.CreateBucketIfNotExists([]byte(c.Config.Bucket))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	go c.Reap()
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *BoltDBCache) Store(cacheKey string, data string, ttl int64) error {

	expKey, dataKey := c.getKeyNames(cacheKey)
	expiration := []byte(strconv.FormatInt(time.Now().Unix()+ttl, 10))

	err := c.dbh.Update(func(tx *bolt.Tx) error {

		b := tx.Bucket([]byte(c.Config.Bucket))

		err := b.Put([]byte(dataKey), []byte(data))
		if err != nil {
			return err
		}

		err = b.Put([]byte(expKey), expiration)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	level.Debug(c.T.Logger).Log("event", "boltdb cache store", "key", dataKey, "expKey", expKey)

	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *BoltDBCache) Retrieve(cacheKey string) (string, error) {

	level.Debug(c.T.Logger).Log("event", "boltdb cache retrieve", "key", cacheKey)

	_, dataKey := c.getKeyNames(cacheKey)

	c.checkExpiration(cacheKey)

	return c.retrieve(dataKey)
}

// retrieve looks for an object in cache and returns it (or an error if not found)
func (c *BoltDBCache) retrieve(cacheKey string) (string, error) {

	content := ""

	err := c.dbh.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.Bucket))
		v := b.Get([]byte(cacheKey))
		if v == nil {
			level.Debug(c.T.Logger).Log("event", "boltdb cache miss", "key", cacheKey)
			return fmt.Errorf("Value for key [%s] not in cache", cacheKey)
		}
		content = string(v)
		return nil
	})
	if err != nil {
		return "", err
	}

	return content, nil
}

// checkExpiration verifies that a cacheKey is not expired
func (c *BoltDBCache) checkExpiration(cacheKey string) {

	expKey, _ := c.getKeyNames(cacheKey)

	content, err := c.retrieve(expKey)
	if err == nil {
		// We found this key, let's see if it's expired
		expiration, err := strconv.ParseInt(string(content), 10, 64)
		if err != nil || expiration < time.Now().Unix() {
			c.Delete(cacheKey)
		}
	}
}

// Delete removes an object in cache, if present
func (c *BoltDBCache) Delete(cacheKey string) error {

	level.Debug(c.T.Logger).Log("event", "boltdb cache delete", "key", cacheKey)

	expKey, dataKey := c.getKeyNames(cacheKey)

	return c.dbh.Update(func(tx *bolt.Tx) error {

		b := tx.Bucket([]byte(c.Config.Bucket))

		err1 := b.Delete([]byte(expKey))
		if err1 != nil {
			level.Error(c.T.Logger).Log("event", "boltdb cache key delete failure", "key", expKey, "reason", err1.Error())
		}

		err2 := b.Delete([]byte(dataKey))
		if err2 != nil {
			level.Error(c.T.Logger).Log("event", "boltdb cache key delete faiure", "key", dataKey, "reason", err2.Error())
		}

		c.T.ChannelCreateMtx.Lock()

		// Close out the channel if it exists
		if _, ok := c.T.ResponseChannels[cacheKey]; ok {
			close(c.T.ResponseChannels[cacheKey])
			delete(c.T.ResponseChannels, cacheKey)
		}

		// Unlock
		c.T.ChannelCreateMtx.Unlock()

		if err1 != nil {
			return err1
		}
		if err2 != nil {
			return err2
		}

		return nil

	})

}

// Reap continually iterates through the cache to find expired elements and removes them
func (c *BoltDBCache) Reap() {

	for {
		c.ReapOnce()
		time.Sleep(time.Duration(c.T.Config.Caching.ReapSleepMS) * time.Millisecond)
	}

}

func (c *BoltDBCache) ReapOnce() {

	now := time.Now().Unix()
	expiredKeys := make([]string, 0)

	// Iterate through the cache to find any expiration keys and check their value
	c.dbh.View(func(tx *bolt.Tx) error {
		// Assume bucket exists and has keys
		b := tx.Bucket([]byte(c.Config.Bucket))
		cursor := b.Cursor()

		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {

			expKey := string(k)

			if strings.HasSuffix(expKey, ".expiration") {

				expiration, err := strconv.ParseInt(string(v), 10, 64)
				if err != nil || expiration < now {

					expiredKeys = append(expiredKeys, strings.Replace(expKey, ".expiration", "", -1))

				}
			}
		}

		return nil
	})

	// Iterate through the expired keys so we can delete them
	for _, cacheKey := range expiredKeys {

		c.Delete(cacheKey)
	}

}

// Close closes the BoltDBCache
func (c *BoltDBCache) Close() error {
	c.dbh.Close()
	return nil
}

func (c *BoltDBCache) getKeyNames(cacheKey string) (string, string) {
	return cacheKey + ".expiration", cacheKey + ".data"
}
