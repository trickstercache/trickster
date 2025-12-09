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

// Package authenticator provides an Authenticator implementation for ClickHouse.
// ClickHouse auth an extension of Basic Auth, so this wraps the Basic Auth
// authenticator with custom Set/ExtractCredentials functions to support the
// ClickHouse extensions
package authenticator

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

const ID types.Provider = providers.ClickHouse

const (
	upUser     = "user"
	upPassword = "password"
)

type authenticator struct {
	*basic.Authenticator
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{Provider: ID, New: New}
}

func New(data map[string]any) (types.Authenticator, error) {
	ba, err := basic.NewPtr(data)
	if err != nil {
		return nil, err
	}
	a := &authenticator{Authenticator: ba}
	ba.SetExtractCredentialsFunc(a.extractCredentials)
	ba.SetSetCredentialsFunc(a.setCredentials)
	return a, nil
}

func (a *authenticator) setCredentials(r *http.Request,
	user, credential string) error {
	q := r.URL.Query()
	if q.Has(upUser) && q.Has(upPassword) {
		q.Set(upUser, user)
		q.Set(upPassword, credential)
		r.URL.RawQuery = q.Encode()
	} else {
		r.SetBasicAuth(user, credential)
	}
	return nil
}

func (a *authenticator) extractCredentials(r *http.Request) (string,
	string, error) {
	q := r.URL.Query()
	if q.Has(upUser) && q.Has(upPassword) {
		return q.Get(upUser), q.Get(upPassword), nil
	}
	u, p, ok := r.BasicAuth()
	if !ok {
		return "", "", ae.ErrInvalidCredentials
	}
	return u, p, nil
}
