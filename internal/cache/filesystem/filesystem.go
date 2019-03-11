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

package filesystem

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache describes a Filesystem Cache
type Cache struct {
	Name     string
	Config   *config.CachingConfig
	mutexes  map[string]*sync.Mutex
	mapMutex sync.Mutex
}

// Configuration returns the Configuration for the Cache object
func (c *Cache) Configuration() *config.CachingConfig {
	return c.Config
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *Cache) Connect() error {
	log.Info("filesystem cache setup", log.Pairs{"cachePath": c.Config.Filesystem.CachePath})

	if err := makeDirectory(c.Config.Filesystem.CachePath); err != nil {
		return err
	}

	c.mutexes = make(map[string]*sync.Mutex)

	go c.Reap()
	return nil
}

// Store places an object in the cache using the specified key and ttl
func (c *Cache) Store(cacheKey string, data []byte, ttl int64) error {
	expFile, dataFile := c.getFileNames(cacheKey)
	expiration := []byte(strconv.FormatInt(time.Now().Unix()+ttl, 10))

	log.Debug("filesystem cache store", log.Pairs{"key": cacheKey, "expFile": expFile, "dataFile": dataFile})
	mtx := c.getMutex(cacheKey)
	mtx.Lock()
	err1 := ioutil.WriteFile(dataFile, data, os.FileMode(0777))
	err2 := ioutil.WriteFile(expFile, expiration, os.FileMode(0777))
	mtx.Unlock()

	if err1 != nil {
		return err1
	} else if err2 != nil {
		return err2
	}
	return nil
}

// Retrieve looks for an object in cache and returns it (or an error if not found)
func (c *Cache) Retrieve(cacheKey string) ([]byte, error) {
	_, dataFile := c.getFileNames(cacheKey)
	log.Debug("filesystem cache retrieve", log.Pairs{"key": cacheKey, "dataFile": dataFile})

	mtx := c.getMutex(cacheKey)
	mtx.Lock()
	content, err := ioutil.ReadFile(dataFile)
	mtx.Unlock()
	if err != nil {
		return []byte{}, fmt.Errorf("Value for key [%s] not in cache", cacheKey)
	}

	return content, nil
}

// Reap continually iterates through the cache to find expired elements and removes them
func (c *Cache) Reap() {
	for {
		now := time.Now().Unix()

		files, err := ioutil.ReadDir(c.Config.Filesystem.CachePath)
		if err == nil {
			for _, file := range files {
				if strings.HasSuffix(file.Name(), ".expiration") {
					cacheKey := strings.Replace(file.Name(), ".expiration", "", 1)
					expFile, dataFile := c.getFileNames(cacheKey)
					mtx := c.getMutex(cacheKey)
					mtx.Lock()
					content, err := ioutil.ReadFile(expFile)
					if err == nil {
						expiration, err := strconv.ParseInt(string(content), 10, 64)
						if err != nil || expiration < now {
							log.Debug("filesystem cache reap", log.Pairs{"key": cacheKey, "dataFile": dataFile})

							// // Get a lock
							// c.T.ChannelCreateMtx.Lock()

							// // Delete the key
							// os.Remove(expFile)
							// os.Remove(dataFile)

							// // Close out the channel if it exists
							// if _, ok := c.T.ResponseChannels[cacheKey]; ok {
							// 	close(c.T.ResponseChannels[cacheKey])
							// 	delete(c.T.ResponseChannels, cacheKey)
							// }

							// // Unlock
							// c.T.ChannelCreateMtx.Unlock()
						}
					}
					mtx.Unlock()
				}
			}
		}

		time.Sleep(time.Duration(c.Config.ReapIntervalMS) * time.Millisecond)
	}
}

// Close is not used for Cache
func (c *Cache) Close() error {
	return nil
}

func (c *Cache) getFileNames(cacheKey string) (string, string) {
	prefix := strings.Replace(c.Config.Filesystem.CachePath+"/"+cacheKey+".", "//", "/", 1)
	return prefix + "expiration", prefix + "data"
}

func (c *Cache) getMutex(cacheKey string) *sync.Mutex {
	var mtx *sync.Mutex
	var ok bool
	c.mapMutex.Lock()
	if mtx, ok = c.mutexes[cacheKey]; !ok {
		mtx = &sync.Mutex{}
		c.mutexes[cacheKey] = mtx
	}
	c.mapMutex.Unlock()

	return mtx
}

// writeable returns true if the path is writeable by the calling process.
func writeable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}

// makeDirectory creates a directory on the filesystem and exits the application in the event of a failure.
func makeDirectory(path string) error {
	err := os.MkdirAll(path, 0755)
	if err != nil || !writeable(path) {
		return fmt.Errorf("[%s] directory is not writeable by the trickster: %v", path, err)
	}

	return nil
}
