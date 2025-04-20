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
)

// Options defines the operation of the Cache Indexer
type Options struct {
	// ReapInterval defines how long the Cache Index reaper sleeps between reap cycles
	ReapInterval time.Duration `yaml:"reap_interval,omitempty"`
	// FlushInterval sets how often the Cache Index saves its metadata to the cache from application memory
	FlushInterval time.Duration `yaml:"flush_interval,omitempty"`
	// MaxSizeBytes indicates how large the cache can grow in bytes before the Index evicts
	// least-recently-accessed items.
	MaxSizeBytes int64 `yaml:"max_size_bytes,omitempty"`
	// MaxSizeBackoffBytes indicates how far below max_size_bytes the cache size must be
	// to complete a byte-size-based eviction exercise.
	MaxSizeBackoffBytes int64 `yaml:"max_size_backoff_bytes,omitempty"`
	// MaxSizeObjects  indicates how large the cache can grow in objects before the Index
	// evicts least-recently-accessed items.
	MaxSizeObjects int64 `yaml:"max_size_objects,omitempty"`
	// MaxSizeBackoffObjects indicates how far under max_size_objects the cache size must
	// be to complete object-size-based eviction exercise.
	MaxSizeBackoffObjects int64 `yaml:"max_size_backoff_objects,omitempty"`
}

// New returns a new Cache Index Options Reference with default values set
func New() *Options {
	return &Options{
		ReapInterval:          DefaultCacheIndexReap,
		FlushInterval:         DefaultCacheIndexFlush,
		MaxSizeBytes:          DefaultCacheMaxSizeBytes,
		MaxSizeBackoffBytes:   DefaultMaxSizeBackoffBytes,
		MaxSizeObjects:        DefaultMaxSizeObjects,
		MaxSizeBackoffObjects: DefaultMaxSizeBackoffObjects,
	}
}

// Equal returns true if all members of the subject and provided Options
// are identical
func (o *Options) Equal(o2 *Options) bool {

	if o2 == nil {
		return false
	}

	return o.ReapInterval == o2.ReapInterval &&
		o.FlushInterval == o2.FlushInterval &&
		o.MaxSizeBytes == o2.MaxSizeBytes &&
		o.MaxSizeBackoffBytes == o2.MaxSizeBackoffBytes &&
		o.MaxSizeObjects == o2.MaxSizeObjects &&
		o.MaxSizeBackoffObjects == o2.MaxSizeBackoffObjects
}
