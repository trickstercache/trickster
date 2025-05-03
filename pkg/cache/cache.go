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

// Package cache defines the Trickster cache interfaces and provides
// general cache functionality
package cache

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
)

// ErrKNF represents the error "key not found in cache"
var ErrKNF = errors.New("key not found in cache")

// Cache is the interface for the supported caching fabrics
// When making new cache providers, Retrieve() must return an error on cache miss
type Cache interface {
	Connect() error
	Store(cacheKey string, data []byte, ttl time.Duration) error
	Retrieve(cacheKey string) ([]byte, status.LookupStatus, error)
	Remove(cacheKeys ...string) error
	Close() error
	Configuration() *options.Options
	Locker() locks.NamedLocker
	SetLocker(locks.NamedLocker)
}

type Lookup map[string]Cache

// MemoryCache is the interface for an in-memory cache
// This offers an additional method for storing references to bypass serialization
type MemoryCache interface {
	Client
	StoreReference(cacheKey string, data ReferenceObject, ttl time.Duration) error
	RetrieveReference(cacheKey string) (any, status.LookupStatus, error)
}

// ReferenceObject defines an interface for a cache object possessing the ability to report
// the approximate comprehensive byte size of its members, to assist with cache size management
type ReferenceObject interface {
	Size() int
}

// Client is an interface that defines the methods required for a cache client
// to be used by cache.Cache implementations
// TODO: should this be internal, or merge into Cache?
type Client interface {
	Connect() error
	Store(cacheKey string, data []byte, ttl time.Duration) error
	Retrieve(cacheKey string) ([]byte, status.LookupStatus, error)
	Remove(cacheKeys ...string) error
	Close() error
}
