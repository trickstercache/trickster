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
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/util/files"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

type Options struct {
	Name            string                      `yaml:"-"` // populated from the Lookup key
	Provider        types.Provider              `yaml:"provider"`
	ObserveOnly     bool                        `yaml:"observe_only"`
	ProxyPreserve   bool                        `yaml:"proxy_preserve"`
	UsersFile       string                      `yaml:"users_file"`
	UsersFileFormat types.CredentialsFileFormat `yaml:"users_file_format"`
	Users           ct.EnvStringMap             `yaml:"users,omitempty"`
	ProviderData    map[string]any              `yaml:"config"`
	Authenticator   types.Authenticator         `yaml:"-"`
}

const redacted = "*****"

// secretNameParts mark a provider config key whose value the config views redact.
var secretNameParts = []string{"secret", "key", "token", "password"}

// Lookup is a map of Options keyed by Options Name
type Lookup map[string]*Options

// New returns a new Authenticator Options with default values
func New() *Options {
	return &Options{}
}

func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	out.Users = maps.Clone(o.Users)
	out.ProviderData = maps.Clone(o.ProviderData)
	return out
}

// CloneYAMLSafe returns a clone with user names and credentials redacted.
func (o *Options) CloneYAMLSafe() *Options {
	out := o.Clone()
	userNames := make([]string, 0, len(out.Users))
	for userName := range out.Users {
		userNames = append(userNames, userName)
	}
	slices.Sort(userNames)
	out.Users = make(ct.EnvStringMap, len(userNames))
	for i := range userNames {
		out.Users[fmt.Sprintf("user%d", i+1)] = redacted
	}
	out.ProviderData = o.RedactedProviderData()
	return out
}

// RedactedProviderData returns a deep copy of the provider config with the values of
// secret-looking keys redacted, at any depth.
func (o *Options) RedactedProviderData() map[string]any {
	return redactProviderData(o.ProviderData)
}

func redactProviderData(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if isSecretName(k) {
			out[k] = redacted
			continue
		}
		out[k] = redactProviderValue(v)
	}
	return out
}

func redactProviderValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return redactProviderData(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = redactProviderValue(t[i])
		}
		return out
	}
	return v
}

func isSecretName(name string) bool {
	name = strings.ToLower(name)
	for _, part := range secretNameParts {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func (o *Options) Initialize() error {
	return nil
}

func (o *Options) Validate(f types.IsRegisteredFunc) error {
	if o.Name == "" || reserved.IsReference(o.Name) {
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
	if (o.UsersFile != "" || o.UsersFileFormat != "") &&
		!types.IsValidCredentialsFileFormat(o.UsersFileFormat) {
		return fmt.Errorf("%w: %q", ae.ErrInvalidUsersFileFormat, o.UsersFileFormat)
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

func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
