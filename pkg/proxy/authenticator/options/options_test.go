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
	"errors"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

const (
	testAuthenticatorName                    = "example"
	testAuthenticatorProvider types.Provider = "test"
)

func isTestProvider(p types.Provider) bool {
	return p == testAuthenticatorProvider
}

func TestValidate(t *testing.T) {
	for _, name := range []string{"", reserved.ReferenceNone} {
		o := &Options{Name: name, Provider: testAuthenticatorProvider}
		if err := o.Validate(isTestProvider); !errors.Is(err, ae.ErrInvalidName) {
			t.Errorf("Validate(%q) = %v; want %v", name, err, ae.ErrInvalidName)
		}
	}
	l := Lookup{reserved.ReferenceNone: {Provider: testAuthenticatorProvider}}
	if err := l.Validate(isTestProvider); !errors.Is(err, ae.ErrInvalidName) {
		t.Errorf("an authenticator named %q = %v; want %v", reserved.ReferenceNone, err, ae.ErrInvalidName)
	}
	l = Lookup{testAuthenticatorName: {Provider: testAuthenticatorProvider}}
	if err := l.Validate(isTestProvider); err != nil || l[testAuthenticatorName].Name != testAuthenticatorName {
		t.Errorf("Lookup.Validate = %v, name %q; want nil, %q", err, l[testAuthenticatorName].Name, testAuthenticatorName)
	}
	o := &Options{Name: testAuthenticatorName, Provider: "unregistered"}
	if err := o.Validate(isTestProvider); !errors.Is(err, ae.ErrInvalidProvider) {
		t.Errorf("unregistered provider = %v; want %v", err, ae.ErrInvalidProvider)
	}
	o = &Options{Name: testAuthenticatorName, Provider: testAuthenticatorProvider,
		UsersFile: filepath.Join(t.TempDir(), "missing")}
	if err := o.Validate(isTestProvider); !errors.Is(err, ae.ErrInvalidUsersFile) {
		t.Errorf("missing users file = %v; want %v", err, ae.ErrInvalidUsersFile)
	}
}

func TestCloneYAMLSafe(t *testing.T) {
	o := &Options{Users: ct.EnvStringMap{
		"bob":   "bob-password",
		"alice": "alice-password",
	}}

	got := o.CloneYAMLSafe()

	if len(got.Users) != 2 ||
		got.Users["user1"] != "*****" ||
		got.Users["user2"] != "*****" {
		t.Fatalf("unexpected redacted users: %#v", got.Users)
	}
	if o.Users["alice"] != "alice-password" || o.Users["bob"] != "bob-password" {
		t.Fatalf("CloneYAMLSafe mutated original users: %#v", o.Users)
	}
}
