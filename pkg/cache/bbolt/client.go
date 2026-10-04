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

package bbolt

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
)

var _ cache.Client = (*CacheClient)(nil)

// CacheClient is a cache.Client that keeps every object in a single bbolt database file
type CacheClient struct {
	*blob.Client
	Name   string
	Config *options.Options
}

// New returns a CacheClient for the named cache, over a Store of the same configuration.
// A fileName or bucketName that is not empty takes the place of the configured one.
func New(cacheName, fileName, bucketName string, opts *options.Options) *CacheClient {
	s := NewStore(cacheName, fileName, bucketName, opts)
	return &CacheClient{
		Client: blob.NewClient(s, cacheName, providers.BBolt),
		Name:   cacheName,
		Config: s.Config,
	}
}
