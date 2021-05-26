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

// Package origins the interface and generic functionality for Origin Types
package origins

import (
	"net/http"

	oo "github.com/trickstercache/trickster/pkg/proxy/origins/options"
)

// Origins represents a map of Origin Clients keyed by Client Name
type Origins map[string]Client

// Get returns the named origin
func (o Origins) Get(originName string) Client {
	if c, ok := o[originName]; ok {
		return c
	}
	return nil
}

// GetConfig returns the named origin's Configuration Options
func (o Origins) GetConfig(originName string) *oo.Options {
	if c, ok := o[originName]; ok {
		return c.Configuration()
	}
	return nil
}

// GetRouter returns the named origin's Request Router
func (o Origins) GetRouter(originName string) http.Handler {
	if c, ok := o[originName]; ok {
		return c.Router()
	}
	return nil
}
