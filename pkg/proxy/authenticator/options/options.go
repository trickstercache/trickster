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
	"maps"

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/util/files"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

var restrictedNames = sets.New([]string{"", "none"})

type Options struct {
	Name            string                      `yaml:"-"` // populated from the Lookup key
	Provider        types.Provider              `yaml:"provider"`
	ProxyPreserve   bool                        `yaml:"proxy_preserve"`
	UsersFile       string                      `yaml:"users_file"`
	UsersFileFormat types.CredentialsFileFormat `yaml:"users_file_format"`
	Users           ct.EnvStringMap             `yaml:"users,omitempty"`
	UsersFormat     types.CredentialsFormat     `yaml:"users_format"`
	ProviderData    map[string]any              `yaml:"config"`
	Authenticator   types.Authenticator
}

// Lookup is a map of Options keyed by Options Name
type Lookup map[string]*Options

func (o *Options) Clone() *Options {
	out := &Options{
		Name:            o.Name,
		Provider:        o.Provider,
		UsersFile:       o.UsersFile,
		UsersFileFormat: o.UsersFileFormat,
		UsersFormat:     o.UsersFormat,
		Authenticator:   o.Authenticator,
		Users:           maps.Clone(o.Users),
		ProviderData:    maps.Clone(o.ProviderData),
		ProxyPreserve:   o.ProxyPreserve,
	}
	return out
}

func (o *Options) Validate(f types.IsRegisteredFunc) error {
	if restrictedNames.Contains(o.Name) {
		return ae.ErrInvalidName
	}
	if !f(o.Provider) {
		return ae.ErrInvalidProvider
	}
	if o.UsersFile != "" {
		if !files.FileExistsAndReadable(o.UsersFile) {
			return ae.ErrInvalidUsersFile
		}
	}
	if len(o.Users) > 0 && o.UsersFormat == "" {
		o.UsersFormat = types.Unknown
	}
	return nil
}

func (l Lookup) Validate(f types.IsRegisteredFunc) error {
	for k, o := range l {
		o.Name = k
		if err := o.Validate(f); err != nil {
			return err
		}
	}
	return nil
}
