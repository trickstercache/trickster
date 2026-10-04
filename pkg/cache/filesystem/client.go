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

package filesystem

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
)

var _ cache.Client = (*CacheClient)(nil)

// CacheClient is a cache.Client that keeps each object in a file of its own
type CacheClient struct {
	*blob.Client
	Name   string
	Config *options.Options
}

// NewCache returns a CacheClient for the named cache, over a Store of the same configuration
func NewCache(name string, config *options.Options) *CacheClient {
	return &CacheClient{
		Client: blob.NewClient(NewStore(name, config), name, providers.Filesystem),
		Name:   name,
		Config: config,
	}
}
