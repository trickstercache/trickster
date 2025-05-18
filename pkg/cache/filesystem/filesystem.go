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

// Package filesystem is the filesystem implementation of the Trickster Cache
package filesystem

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

var (
	// CacheClient implements the cache.Client interface
	_ cache.Client = &CacheClient{}
)

func NewCache(name string, config *options.Options) *CacheClient {
	c := &CacheClient{
		Name:   name,
		Config: config,
	}

	return c
}

// CacheClient describes a Filesystem CacheClient
type CacheClient struct {
	Name   string
	Config *options.Options
}

func (c *CacheClient) Close() error {
	return nil
}

// Connect instantiates the Cache mutex map and starts the Expired Entry Reaper goroutine
func (c *CacheClient) Connect() error {
	return makeDirectory(c.Config.Filesystem.CachePath)
}

func (c *CacheClient) Remove(cacheKeys ...string) error {
	for _, cacheKey := range cacheKeys {
		if err := os.Remove(c.getFileName(cacheKey)); err != nil {
			return err
		}
	}
	return nil
}

func (c *CacheClient) Store(cacheKey string, data []byte, ttl time.Duration) error {
	if ttl < 1 {
		return fmt.Errorf("invalid ttl: %d", int64(ttl.Seconds()))
	}
	if cacheKey == "" {
		return errors.New("cacheKey required")
	}
	dataFile := c.getFileName(cacheKey)
	err := os.WriteFile(dataFile, data, os.FileMode(0777))
	if err != nil {
		return err
	}
	return nil
}

func (c *CacheClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	dataFile := c.getFileName(cacheKey)
	data, err := os.ReadFile(dataFile)
	if err != nil {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return data, status.LookupStatusHit, nil
}

func (c *CacheClient) getFileName(cacheKey string) string {
	prefix := strings.Replace(c.Config.Filesystem.CachePath+"/"+cacheKey+".", "//", "/", 1)
	return prefix + "data"
}

// makeDirectory creates a directory on the filesystem and returns the error in the event of a failure.
func makeDirectory(path string) error {
	err := os.MkdirAll(path, 0755)
	if err == nil {
		s := ""
		if !strings.HasSuffix(path, "/") {
			s = "/"
		}
		// verify writability by attempting to touch a test file in the cache path
		tf := path + s + ".test." + strconv.FormatInt(time.Now().Unix(), 10)
		err = os.WriteFile(tf, []byte(""), 0600)
		if err == nil {
			os.Remove(tf)
		}
	}
	if err != nil {
		return fmt.Errorf("[%s] directory is not writeable by trickster: %v", path, err)
	}
	return nil
}
