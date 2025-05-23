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

package basic

import (
	"fmt"
	"maps"
	"net/http"

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/loaders"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"golang.org/x/crypto/bcrypt"
)

const ID types.Provider = "basic"

const showLoginFormField = "showLoginForm"
const optionsField = "options"
const realmField = "realm"

type Authenticator struct {
	users            types.CredentialsManifest
	extractCredsFunc types.ExtractCredsFunc
	showLoginForm    bool
	realm            string
	proxyPreserve    bool
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{Provider: ID, New: New}
}

func New(data map[string]any) (types.Authenticator, error) {
	var opts *options.Options
	if data != nil {
		if v, ok := data[optionsField]; ok && v != nil {
			opts, _ = v.(*options.Options)
		}
	}
	if opts == nil {
		return nil, errors.ErrInvalidOptions
	}
	a := &Authenticator{realm: opts.Name, proxyPreserve: opts.ProxyPreserve}
	if len(opts.ProviderData) > 0 {
		if v, ok := opts.ProviderData[showLoginFormField]; ok {
			if t, ok := v.(bool); ok && t {
				a.showLoginForm = true
			}
		}
		if a.showLoginForm {
			if v, ok := opts.ProviderData[realmField]; ok {
				if s, ok := v.(string); ok && s != "" {
					a.realm = s
				}
			}
		}
	}
	if opts.UsersFile != "" {
		err := a.LoadUsers(opts.UsersFile, opts.UsersFileFormat, opts.UsersFormat, true)
		if err != nil {
			return nil, err
		}
	}
	if len(opts.Users) > 0 {
		a.AddUsersFromMap(esLookup(opts.Users), opts.UsersFormat)
	}
	return a, nil
}

func failureHeader(showLoginForm bool, realm string) map[string]string {
	if !showLoginForm {
		return nil
	}
	return map[string]string{
		headers.NameWWWAuthenticate: fmt.Sprintf(`Basic realm="%s"`, realm)}
}

func failedResult(showLoginForm bool, realm string) *types.AuthResult {
	return &types.AuthResult{
		Status:          types.AuthFailed,
		ResponseHeaders: failureHeader(showLoginForm, realm),
	}
}

// Authenticate checks the BasicAuth credentials
func (a *Authenticator) Authenticate(r *http.Request) (*types.AuthResult, error) {
	u, p, f, err := a.ExtractCredentials(r)
	if err != nil {
		return failedResult(a.showLoginForm, a.realm), err
	}
	if f != types.PlainText {
		return failedResult(a.showLoginForm, a.realm), ae.ErrInvalidCredentialsFormat
	}
	hash, ok := a.users[u]
	if !ok {
		return failedResult(a.showLoginForm, a.realm), ae.ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) != nil {
		return failedResult(a.showLoginForm, a.realm), ae.ErrInvalidCredentials
	}
	return &types.AuthResult{Username: u, Status: types.AuthSuccess}, nil
}

// Clone clones a new Authenticator (i) from a
func (a *Authenticator) Clone() types.Authenticator {
	return a.ClonePtr()
}

// Clone returns a new, completely independent clone of the Authenticator
func (a *Authenticator) ClonePtr() *Authenticator {
	out := &Authenticator{}
	if a.users != nil {
		out.users = make(types.CredentialsManifest, len(a.users))
		maps.Copy(out.users, a.users)
	}
	if a.extractCredsFunc != nil {
		out.extractCredsFunc = a.extractCredsFunc
	}
	out.showLoginForm = a.showLoginForm
	out.realm = a.realm
	return out
}

func (a *Authenticator) ProxyPreserve() bool {
	return a.proxyPreserve
}

func (a *Authenticator) Sanitize(r *http.Request) {
	if a.proxyPreserve {
		return
	}
	r.Header.Del(headers.NameAuthorization)
}

func (a *Authenticator) ExtractCredentials(r *http.Request) (string, string,
	types.CredentialsFormat, error) {
	if a.extractCredsFunc != nil {
		return a.extractCredsFunc(r)
	}
	u, p, ok := r.BasicAuth()
	if !ok {
		return "", "", "", ae.ErrInvalidCredentials
	}
	return u, p, types.PlainText, nil
}

func (a *Authenticator) SetExtractCredentialsFunc(f types.ExtractCredsFunc) {
	a.extractCredsFunc = f
}

// LoadUsers resets the users list, then loads from the htpasswd-formatted file
func (a *Authenticator) LoadUsers(path string, ff types.CredentialsFileFormat,
	cf types.CredentialsFormat, replace bool) error {
	users, err := loaders.LoadData(path, ff, cf)
	if err != nil {
		return err
	}
	if replace || a.users == nil {
		a.users = users
	} else {
		maps.Copy(a.users, users)
	}
	return nil
}

// AddUser adds a new user to the users list
func (a *Authenticator) AddUser(username, password string,
	cf types.CredentialsFormat) error {
	p, err := cred.ProcessRawCredential(password, cf)
	if err != nil {
		return err
	}
	if a.users == nil {
		a.users = make(types.CredentialsManifest)
	}
	a.users[username] = p
	return nil
}

// RemoveUser removes a user from the users list
func (a *Authenticator) RemoveUser(username string) {
	if a.users == nil {
		return
	}
	delete(a.users, username)
}

// AddUsersFromMap merges in users from a map[string]any
func (a *Authenticator) AddUsersFromMap(users esLookup,
	cf types.CredentialsFormat) {
	loaded := loaders.LoadMap(users.ToCredentialsManifest(), cf)
	if a.users == nil {
		a.users = loaded
	} else {
		maps.Copy(a.users, loaded)
	}
}

// LoadUsersFromMap resets the users list, then loads from a map[string]any
// via AddUsersFromMap
func (a *Authenticator) LoadUsersFromMap(users esLookup,
	cf types.CredentialsFormat) {
	a.users = loaders.LoadMap(users.ToCredentialsManifest(), cf)
}

type esLookup ct.EnvStringMap

func (l esLookup) ToCredentialsManifest() types.CredentialsManifest {
	out := make(types.CredentialsManifest, len(l))
	for k, v := range l {
		out[k] = string(v)
	}
	return out
}
