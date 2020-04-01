/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package options

import (
	badger "github.com/tricksterproxy/trickster/pkg/cache/badger/options"
	bbolt "github.com/tricksterproxy/trickster/pkg/cache/bbolt/options"
	filesystem "github.com/tricksterproxy/trickster/pkg/cache/filesystem/options"
	index "github.com/tricksterproxy/trickster/pkg/cache/index/options"
	redis "github.com/tricksterproxy/trickster/pkg/cache/redis/options"
	"github.com/tricksterproxy/trickster/pkg/cache/types"
	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
)

// Options is a collection of defining the Trickster Caching Behavior
type Options struct {
	// Name is the Name of the cache, taken from the Key in the Caches map[string]*CacheConfig
	Name string `toml:"-"`
	// Type represents the type of cache that we wish to use: "boltdb", "memory", "filesystem", or "redis"
	CacheType string `toml:"cache_type"`
	// Index provides options for the Cache Index
	Index *index.Options `toml:"index"`
	// Redis provides options for Redis caching
	Redis *redis.Options `toml:"redis"`
	// Filesystem provides options for Filesystem caching
	Filesystem *filesystem.Options `toml:"filesystem"`
	// BBolt provides options for BBolt caching
	BBolt *bbolt.Options `toml:"bbolt"`
	// Badger provides options for BadgerDB caching
	Badger *badger.Options `toml:"badger"`

	//  Synthetic Values

	// CacheTypeID represents the internal constant for the provided CacheType string
	// and is automatically populated at startup
	CacheTypeID types.CacheType `toml:"-"`
}

// NewOptions will return a pointer to an OriginConfig with the default configuration settings
func NewOptions() *Options {

	return &Options{
		CacheType:   d.DefaultCacheType,
		CacheTypeID: d.DefaultCacheTypeID,
		Redis:       redis.NewOptions(),
		Filesystem:  filesystem.NewOptions(),
		BBolt:       bbolt.NewOptions(),
		Badger:      badger.NewOptions(),
		Index:       index.NewOptions(),
	}
}

// Clone returns an exact copy of a *CachingConfig
func (cc *Options) Clone() *Options {

	c := NewOptions()
	c.Name = cc.Name
	c.CacheType = cc.CacheType
	c.CacheTypeID = cc.CacheTypeID

	c.Index.FlushInterval = cc.Index.FlushInterval
	c.Index.FlushIntervalSecs = cc.Index.FlushIntervalSecs
	c.Index.MaxSizeBackoffBytes = cc.Index.MaxSizeBackoffBytes
	c.Index.MaxSizeBackoffObjects = cc.Index.MaxSizeBackoffObjects
	c.Index.MaxSizeBytes = cc.Index.MaxSizeBytes
	c.Index.MaxSizeObjects = cc.Index.MaxSizeObjects
	c.Index.ReapInterval = cc.Index.ReapInterval
	c.Index.ReapIntervalSecs = cc.Index.ReapIntervalSecs

	c.Badger.Directory = cc.Badger.Directory
	c.Badger.ValueDirectory = cc.Badger.ValueDirectory

	c.Filesystem.CachePath = cc.Filesystem.CachePath

	c.BBolt.Bucket = cc.BBolt.Bucket
	c.BBolt.Filename = cc.BBolt.Filename

	c.Redis.ClientType = cc.Redis.ClientType
	c.Redis.DB = cc.Redis.DB
	c.Redis.DialTimeoutMS = cc.Redis.DialTimeoutMS
	c.Redis.Endpoint = cc.Redis.Endpoint
	c.Redis.Endpoints = cc.Redis.Endpoints
	c.Redis.IdleCheckFrequencyMS = cc.Redis.IdleCheckFrequencyMS
	c.Redis.IdleTimeoutMS = cc.Redis.IdleTimeoutMS
	c.Redis.MaxConnAgeMS = cc.Redis.MaxConnAgeMS
	c.Redis.MaxRetries = cc.Redis.MaxRetries
	c.Redis.MaxRetryBackoffMS = cc.Redis.MaxRetryBackoffMS
	c.Redis.MinIdleConns = cc.Redis.MinIdleConns
	c.Redis.MinRetryBackoffMS = cc.Redis.MinRetryBackoffMS
	c.Redis.Password = cc.Redis.Password
	c.Redis.PoolSize = cc.Redis.PoolSize
	c.Redis.PoolTimeoutMS = cc.Redis.PoolTimeoutMS
	c.Redis.Protocol = cc.Redis.Protocol
	c.Redis.ReadTimeoutMS = cc.Redis.ReadTimeoutMS
	c.Redis.SentinelMaster = cc.Redis.SentinelMaster
	c.Redis.WriteTimeoutMS = cc.Redis.WriteTimeoutMS

	return c

}
