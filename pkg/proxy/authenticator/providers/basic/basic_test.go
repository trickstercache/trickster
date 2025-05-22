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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"maps"

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"golang.org/x/crypto/bcrypt"
)

const testUser1 = "testUser1"
const testUser1p = "testUser1p"

const testUser2 = "testUser2"
const testUser2p = "testUser2p"

const testUser3 = "testUser3"
const testUser3p = "testUser3p"

func bcryptHash(pass string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(h)
}

func TestAddUserAndAuthenticate(t *testing.T) {
	a := &Authenticator{}
	user, pass := testUser1, testUser1p
	if err := a.AddUser(user, pass, types.PlainText); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(user, pass)
	ar, err := a.Authenticate(req)
	if err != nil {
		t.Error(err)
	}
	if ar == nil || ar.Username != user {
		t.Errorf("expected %s got %s", user, ar.Username)
	}

	// Bad password
	req.SetBasicAuth(user, "wrong")
	_, err = a.Authenticate(req)
	if err == nil {
		t.Error("expected unauthorized error")
	}

	// Non-existent user
	req.SetBasicAuth("invalid", "invalid")
	_, err = a.Authenticate(req)
	if err == nil {
		t.Error("expected unauthorized error")
	}

	// No credentials
	req = httptest.NewRequest("GET", "/", nil)
	_, err = a.Authenticate(req)
	if err == nil {
		t.Error("expected unauthorized error")
	}
}

func TestRemoveUser(t *testing.T) {
	a := &Authenticator{}
	a.AddUser(testUser1, testUser1p, types.PlainText)
	a.RemoveUser(testUser1)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(testUser1, testUser1p)
	_, err := a.Authenticate(req)
	if err == nil {
		t.Error("expected unauthorized error")
	}
}

func TestAddUsersFromMapLoadUsersFromMap(t *testing.T) {
	a := &Authenticator{}
	users := map[string]ct.EnvString{
		testUser1: testUser1p,
		testUser2: testUser2p,
	}
	a.AddUsersFromMap(users, types.PlainText) // not encrypted

	// Should authenticate both
	for user, pass := range users {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth(user, string(pass))
		_, err := a.Authenticate(req)
		if err != nil {
			t.Errorf("Authenticate failed for %s: %v", user, err)
		}
	}

	// Now replace with LoadUsersFromMap and test
	newUsers := map[string]ct.EnvString{
		testUser3: testUser3p,
	}
	a.LoadUsersFromMap(newUsers, types.PlainText)
	// Only linus should work
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(testUser3, testUser3p)
	_, err := a.Authenticate(req)
	if err != nil {
		t.Errorf("Authenticate linus after LoadUsersFromMap: %v", err)
	}
	req.SetBasicAuth(testUser1, testUser1p)
	_, err = a.Authenticate(req)
	if err == nil {
		t.Error("Authenticate charlie after LoadUsersFromMap should fail")
	}
}

func TestAddUsersFromMap_Encrypted(t *testing.T) {
	a := &Authenticator{}
	hash := bcryptHash(testUser1p)
	users := map[string]ct.EnvString{
		testUser1: ct.EnvString(hash),
	}
	a.AddUsersFromMap(users, types.BCrypt) // already encrypted

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(testUser1, testUser1p)
	_, err := a.Authenticate(req)
	if err != nil {
		t.Errorf("Authenticate schroeder with encrypted: %v", err)
	}
}

func TestLoadUsersAndAddUsers_File(t *testing.T) {
	tempDir := t.TempDir()

	// Write a temp .htpasswd file
	htpasswd1 := filepath.Join(tempDir, "htpasswd")
	f, err := os.Create(htpasswd1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(fmt.Sprintf("%s:%s\n", testUser1, bcryptHash(testUser1p)))
	f.Close()

	a := &Authenticator{}
	if err := a.LoadUsers(htpasswd1, types.HTPasswd, types.BCrypt, true); err != nil {
		t.Fatal(err)
	}
	// testUser1 should work
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(testUser1, testUser1p)
	_, err = a.Authenticate(req)
	if err != nil {
		t.Error(err)
	}

	// Add another user via AddUsers (merge)
	htpasswd2 := filepath.Join(tempDir, "htpasswd2")
	f2, err := os.Create(htpasswd2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f2.WriteString(fmt.Sprintf("%s:%s\n", testUser2, bcryptHash(testUser2p)))
	f2.Close()
	if err := a.LoadUsers(htpasswd2, types.HTPasswd, types.BCrypt, false); err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser2, testUser2p)
	_, err = a.Authenticate(req)
	if err != nil {
		t.Error(err)
	}
}

func TestAddUsers_MergeLogic(t *testing.T) {
	a := &Authenticator{}
	_ = a.AddUser(testUser1, testUser1p, types.PlainText)
	// Simulate AddUsers merges and updates
	users := map[string]string{
		testUser1: bcryptHash("updated"), // should replace
		testUser2: bcryptHash(testUser2p),
	}
	if a.users == nil {
		a.users = make(map[string]string)
	}
	maps.Copy(a.users, users)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(testUser1, "updated")
	_, err := a.Authenticate(req)
	if err != nil {
		t.Error(err)
	}
	// old password should fail
	req.SetBasicAuth(testUser1, testUser1p)
	_, err = a.Authenticate(req)
	if err == nil {
		t.Error("expected unauthorized error")
	}
	// user 2 patty should work
	req.SetBasicAuth(testUser2, testUser2p)
	_, err = a.Authenticate(req)
	if err != nil {
		t.Error(err)
	}
}
