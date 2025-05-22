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

package registry

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

var ErrUnsupportedAuthenticator = errors.New("unsupported authenticator")

// this slice is the one and only place to aggregate all registered Authenticators
var registry = []types.RegistryEntry{
	basic.RegistryEntry(), // ID 0, BasicAuth
}

var registryByName = compileSupportedByName()

func compileSupportedByName() map[types.Provider]types.NewAuthenticatorFunc {
	out := make(map[types.Provider]types.NewAuthenticatorFunc, len(registry)*2)
	for _, entry := range registry {
		out[entry.Provider] = entry.New
	}
	return out
}

func New(name types.Provider, data map[string]any) (types.Authenticator, error) {
	if f, ok := registryByName[name]; ok && f != nil {
		return f(data)
	}
	return nil, ErrUnsupportedAuthenticator
}

func IsRegistered(name types.Provider) bool {
	_, ok := registryByName[name]
	return ok
}
