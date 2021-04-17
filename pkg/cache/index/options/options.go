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

package options

import (
	"time"

	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
)

// Options defines the operation of the Cache Indexer
type Options struct {
	// ReapIntervalSecs defines how long the Cache Index reaper sleeps between reap cycles
	ReapIntervalSecs int `toml:"reap_interval_secs"`
	// FlushIntervalSecs sets how often the Cache Index saves its metadata to the cache from application memory
	FlushIntervalSecs int `toml:"flush_interval_secs"`
	// MaxSizeBytes indicates how large the cache can grow in bytes before the Index evicts
	// least-recently-accessed items.
	MaxSizeBytes int64 `toml:"max_size_bytes"`
	// MaxSizeBackoffBytes indicates how far below max_size_bytes the cache size must be
	// to complete a byte-size-based eviction exercise.
	MaxSizeBackoffBytes int64 `toml:"max_size_backoff_bytes"`
	// MaxSizeObjects  indicates how large the cache can grow in objects before the Index
	// evicts least-recently-accessed items.
	MaxSizeObjects int64 `toml:"max_size_objects"`
	// MaxSizeBackoffObjects indicates how far under max_size_objects the cache size must
	// be to complete object-size-based eviction exercise.
	MaxSizeBackoffObjects int64 `toml:"max_size_backoff_objects"`

	ReapInterval  time.Duration `toml:"-"`
	FlushInterval time.Duration `toml:"-"`
}

// NewOptions returns a new Cache Index Options Reference with default values set
func NewOptions() *Options {
	return &Options{
		ReapIntervalSecs:      d.DefaultCacheIndexReap,
		FlushIntervalSecs:     d.DefaultCacheIndexFlush,
		MaxSizeBytes:          d.DefaultCacheMaxSizeBytes,
		MaxSizeBackoffBytes:   d.DefaultMaxSizeBackoffBytes,
		MaxSizeObjects:        d.DefaultMaxSizeObjects,
		MaxSizeBackoffObjects: d.DefaultMaxSizeBackoffObjects,
	}
}

// Equal returns true if all members of the subject and provided Options
// are identical
func (o *Options) Equal(o2 *Options) bool {

	if o2 == nil {
		return false
	}

	return o.ReapIntervalSecs == o2.ReapIntervalSecs &&
		o.FlushIntervalSecs == o2.FlushIntervalSecs &&
		o.MaxSizeBytes == o2.MaxSizeBytes &&
		o.MaxSizeBackoffBytes == o2.MaxSizeBackoffBytes &&
		o.MaxSizeObjects == o2.MaxSizeObjects &&
		o.MaxSizeBackoffObjects == o2.MaxSizeBackoffObjects
}
