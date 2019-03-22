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

package bbolt

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	bbolt "github.com/coreos/bbolt"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache describes a BBolt Cache
type Cache struct {
	Name   string
	Config *config.CachingConfig
	dbh    *bbolt.DB
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	log.Info("bbolt cache setup", log.Pairs{"cacheFile": c.Config.BBolt.Filename})

	var err error
	c.dbh, err = bbolt.Open(c.Config.BBolt.Filename, 0644, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return err
	}

	err = c.dbh.Update(func(tx *bbolt.Tx) error {
		tx.CreateBucketIfNotExists([]byte(c.Config.BBolt.Bucket))
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
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {

	expKey, dataKey := c.getKeyNames(cacheKey)
	expiration := []byte(strconv.FormatInt(time.Now().Unix()+ttl, 10))

	err := c.dbh.Update(func(tx *bbolt.Tx) error {

		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))

		err := b.Put([]byte(dataKey), data)
		if err != nil {
			return err
		}

		return b.Put([]byte(expKey), expiration)
	})
	if err != nil {
		return err
	}

	log.Debug("bbolt cache store", log.Pairs{"key": dataKey, "expKey": expKey})

	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {

	log.Debug("bbolt cache retrieve", log.Pairs{"key": cacheKey})

	_, dataKey := c.getKeyNames(cacheKey)

	c.checkExpiration(cacheKey)

	return c.retrieve(dataKey)
}

// retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) retrieve(cacheKey string) ([]byte, error) {

	var value []byte

	err := c.dbh.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
		v := b.Get([]byte(cacheKey))
		if v == nil {
			log.Debug("bbolt cache miss", log.Pairs{"key": cacheKey})
			return fmt.Errorf("Value for key [%s] not in cache", cacheKey)
		}
		value = v
		return nil
	})
	if err != nil {
		return nil, err
	}

	return value, nil
}

// checkExpiration verifies that a cacheKey is not expired
func (c *Cache) checkExpiration(cacheKey string) {

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
func (c *Cache) Delete(cacheKey string) error {

	log.Debug("bbolt cache delete", log.Pairs{"key": cacheKey})

	expKey, dataKey := c.getKeyNames(cacheKey)

	return c.dbh.Update(func(tx *bbolt.Tx) error {

		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))

		err1 := b.Delete([]byte(expKey))
		if err1 != nil {
			log.Error("bbolt cache key delete failure", log.Pairs{"key": expKey, "reason": err1.Error()})
		}

		err2 := b.Delete([]byte(dataKey))
		if err2 != nil {
			log.Error("bbolt cache key delete failure", log.Pairs{"key": dataKey, "reason": err2.Error()})
		}

		// c.T.ChannelCreateMtx.Lock()

		// // Close out the channel if it exists
		// if _, ok := c.T.ResponseChannels[cacheKey]; ok {
		// 	close(c.T.ResponseChannels[cacheKey])
		// 	delete(c.T.ResponseChannels, cacheKey)
		// }

		// // Unlock
		// c.T.ChannelCreateMtx.Unlock()

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
func (c *Cache) Reap() {

	for {
		c.ReapOnce()
		time.Sleep(time.Duration(c.Config.ReapIntervalMS) * time.Millisecond)
	}

}

// ReapOnce makes a single iteration through the cache to to find and remove expired elements
func (c *Cache) ReapOnce() {

	now := time.Now().Unix()
	expiredKeys := make([]string, 0)

	// Iterate through the cache to find any expiration keys and check their value
	c.dbh.View(func(tx *bbolt.Tx) error {
		// Assume bucket exists and has keys
		b := tx.Bucket([]byte(c.Config.BBolt.Bucket))
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

// Close closes the Cache
func (c *Cache) Close() error {
	return c.dbh.Close()
}

func (c *Cache) getKeyNames(cacheKey string) (string, string) {
	return cacheKey + ".expiration", cacheKey + ".data"
}
