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

package influxdb

import (
	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
)

// Client Implements the Proxy Client Interface
type Client struct {
	Name   string
	User   string
	Pass   string
	Config config.OriginConfig
	Cache  cache.Cache
}

// Configuration returns the upstream Configuration for this Client
func (c Client) Configuration() config.OriginConfig {
	return c.Config
}

// CacheInstance returns and handle to the Cache instance used by the Client
func (c Client) CacheInstance() cache.Cache {
	return c.Cache
}

// OriginName returns the name of the upstream Configuration proxied by the Client
func (c Client) OriginName() string {
	return c.Name
}
