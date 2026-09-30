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
	"io"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
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

// ObjectMeta is what a stored object says of itself, apart from its content
type ObjectMeta struct {
	Key string
	// Size is the length in bytes of the object's content
	Size int64
	// Expiration is when the object expires, and is zero when it never does
	Expiration time.Time
	LastWrite  time.Time
}

// Scanner is an optional Client capability for listing the objects a cache really
// holds, which is how an index is rebuilt or checked against the cache
type Scanner interface {
	// ScanMeta passes up to limit stored objects to fn, starting past the position that
	// after names, and returns the position to resume from and whether it reached the end
	ScanMeta(after string, limit int, fn func(ObjectMeta)) (next string, done bool, err error)
}

// SplitClient is an optional Client capability for keeping an object as two sections, one
// that describes it and the content itself, so that neither is copied into the other
type SplitClient interface {
	// StoreSplit writes an object of two sections at cacheKey, to expire after ttl
	StoreSplit(cacheKey string, meta, body []byte, ttl time.Duration) error
	// RetrieveSplit returns the two sections of the object stored at cacheKey. An object that
	// was stored whole is returned as a body with no meta.
	RetrieveSplit(cacheKey string) (meta, body []byte, s status.LookupStatus, err error)
	// SupportsSplit reports whether the capability is there to use, which it may not be for
	// a Client that wraps another
	SupportsSplit() bool
}

// Body is the content of a stored object, which is read from the cache only as it is asked
// for. It holds a resource of the cache until it is closed.
type Body interface {
	io.Reader
	io.ReaderAt
	io.Closer
	// Size returns the length of the content in bytes
	Size() int64
	// ReadAll returns the whole of the content, verified against the object's checksum
	ReadAll() ([]byte, error)
}

// StreamClient is an optional Client capability for reading an object's content in parts,
// or as a stream, in place of all at once
type StreamClient interface {
	// OpenSplit returns the meta section of the object stored at cacheKey, and its body
	// unread. The body is not verified against the object's checksum, as it is when retrieved.
	OpenSplit(cacheKey string) (meta []byte, body Body, s status.LookupStatus, err error)
	// SupportsStream reports whether the capability is there to use, which it is only for
	// a cache whose objects may stay open while a slow reader consumes them
	SupportsStream() bool
}

// Client is an interface that defines the methods required for a cache client
// to be used by cache.Cache implementations
type Client interface {
	Connect() error
	Store(cacheKey string, data []byte, ttl time.Duration) error
	Retrieve(cacheKey string) ([]byte, status.LookupStatus, error)
	Remove(cacheKeys ...string) error
	Close() error
}
