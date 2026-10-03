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

package loaders

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func TestLoadDataUnknownFormat(t *testing.T) {
	for _, ff := range []types.CredentialsFileFormat{"", "htpassword"} {
		if _, err := LoadData("users", ff); !errors.Is(err, ae.ErrInvalidUsersFileFormat) {
			t.Errorf("format %q = %v; want %v", ff, err, ae.ErrInvalidUsersFileFormat)
		}
	}
}

func TestLoadDataKnownFormats(t *testing.T) {
	const user, password = "user1", "password1"
	path := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(path, []byte(user+":"+password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	users, err := LoadData(path, types.HTPasswd)
	if err != nil || users[user] != password {
		t.Fatalf("htpasswd = %v, %v", users, err)
	}
	if err := os.WriteFile(path, []byte(user+","+password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	users, err = LoadData(path, types.CSVNoHeader)
	if err != nil || users[user] != password {
		t.Fatalf("csvNoHeader = %v, %v", users, err)
	}
}
