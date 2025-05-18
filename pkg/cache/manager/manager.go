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

package manager

import (
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/locks"
)

type CacheOptions struct {
	UseIndex     bool
	IndexCliOpts index.IndexedClientOptions
}

func NewCache(cli cache.Client, cacheOpts CacheOptions, cacheConfig *options.Options) cache.Cache {
	cm := &Manager{
		Client:      cli,
		originalCli: cli,
		config:      cacheConfig,
		opts:        cacheOpts,
	}
	cm.locker = locks.NewNamedLocker()
	return cm
}

type Manager struct {
	cache.Client
	originalCli cache.Client
	config      *options.Options
	locker      locks.NamedLocker
	opts        CacheOptions
}

func (cm *Manager) Connect() error {
	if err := cm.Client.Connect(); err != nil {
		return err
	}
	if cm.opts.UseIndex {
		cm.Client = index.NewIndexedClient(
			cm.config.Name,
			cm.config.Provider,
			nil,
			cm.config.Index,
			cm.originalCli,
			func(ico *index.IndexedClientOptions) {
				*ico = cm.opts.IndexCliOpts
			},
		)
	}
	return nil
}

func (cm *Manager) Configuration() *options.Options {
	return cm.config
}

func (cm *Manager) Locker() locks.NamedLocker {
	return cm.locker
}
func (cm *Manager) SetLocker(l locks.NamedLocker) {
	cm.locker = l
}
