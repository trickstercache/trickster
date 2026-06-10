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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	pkgerrors "github.com/trickstercache/trickster/v2/pkg/errors"
	authopt "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"golang.org/x/crypto/bcrypt"
)

const (
	testUser1  = "testUser1"
	testUser1p = "testUser1p"
)

const (
	testUser2  = "testUser2"
	testUser2p = "testUser2p"
)

const (
	testUser3  = "testUser3"
	testUser3p = "testUser3p"
)

func bcryptHash(pass string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(h)
}

func TestAddUserAndAuthenticate(t *testing.T) {
	a := &Authenticator{}
	user, pass := testUser1, testUser1p
	hash := bcryptHash(pass)
	if err := a.AddUser(user, hash); err != nil {
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
	hash := bcryptHash(testUser1p)
	a.AddUser(testUser1, hash)
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
	users := ct.EnvStringMap{
		testUser1: bcryptHash(testUser1p),
		testUser2: bcryptHash(testUser2p),
	}
	a.AddUsersFromMap(esLookup(users))

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
	newUsers := ct.EnvStringMap{
		testUser3: bcryptHash(testUser3p),
	}
	a.LoadUsersFromMap(esLookup(newUsers))
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
	users := ct.EnvStringMap{
		testUser1: hash,
	}
	a.AddUsersFromMap(esLookup(users)) // already encrypted (bcrypt hash)

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
	if err := a.LoadUsers(htpasswd1, types.HTPasswd, true); err != nil {
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
	if err := a.LoadUsers(htpasswd2, types.HTPasswd, false); err != nil {
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
	_ = a.AddUser(testUser1, bcryptHash(testUser1p))
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

func TestNewPtr(t *testing.T) {
	t.Parallel()

	_, err := NewPtr(nil)
	if err != pkgerrors.ErrInvalidOptions {
		t.Fatalf("NewPtr(nil) = %v, want ErrInvalidOptions", err)
	}

	opts := authopt.New()
	opts.Name = "default-realm"
	opts.Users = ct.EnvStringMap{testUser1: bcryptHash(testUser1p)}
	a, err := NewPtr(map[string]any{"options": opts})
	if err != nil {
		t.Fatalf("NewPtr: %v", err)
	}
	if a.realm != "default-realm" {
		t.Fatalf("realm = %q", a.realm)
	}

	opts = authopt.New()
	opts.ProviderData = map[string]any{
		showLoginFormField: true,
		realmField:         "Protected Area",
	}
	opts.Users = ct.EnvStringMap{testUser1: bcryptHash(testUser1p)}
	a, err = NewPtr(map[string]any{"options": opts})
	if err != nil {
		t.Fatalf("NewPtr with login form: %v", err)
	}
	if !a.showLoginForm || a.realm != "Protected Area" {
		t.Fatalf("showLoginForm=%v realm=%q", a.showLoginForm, a.realm)
	}
}

func TestAuthenticateObserveOnly(t *testing.T) {
	t.Parallel()

	a := &Authenticator{observeOnly: true}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth(testUser1, testUser1p)
	ar, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if ar.Status != types.AuthObserved || ar.Username != testUser1 {
		t.Fatalf("AuthResult = %+v", ar)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	ar, err = a.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate without creds: %v", err)
	}
	if ar.Status != types.AuthObserved || ar.Username != "" {
		t.Fatalf("AuthResult = %+v", ar)
	}
}

func TestAuthenticateLoginFormFailureHeaders(t *testing.T) {
	t.Parallel()

	a := &Authenticator{showLoginForm: true, realm: "Test Realm"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ar, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if ar == nil || ar.ResponseHeaders[headers.NameWWWAuthenticate] != `Basic realm="Test Realm"` {
		t.Fatalf("ResponseHeaders = %+v", ar)
	}
}

func TestCloneAndSanitize(t *testing.T) {
	t.Parallel()

	a := &Authenticator{
		users:         types.CredentialsManifest{testUser1: bcryptHash(testUser1p)},
		showLoginForm: true,
		realm:         "realm",
		proxyPreserve: false,
	}
	cl := a.ClonePtr()
	if cl == a {
		t.Fatal("ClonePtr should produce independent copy")
	}
	cl.users[testUser1] = "changed"
	if a.users[testUser1] == "changed" {
		t.Fatal("ClonePtr should not share users map")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headers.NameAuthorization, "Basic abc")
	a.Sanitize(req)
	if req.Header.Get(headers.NameAuthorization) != "" {
		t.Fatal("expected Authorization header to be removed")
	}

	a.proxyPreserve = true
	req.Header.Set(headers.NameAuthorization, "Basic abc")
	a.Sanitize(req)
	if req.Header.Get(headers.NameAuthorization) == "" {
		t.Fatal("expected Authorization header to be preserved")
	}
}

func TestCustomCredentialFuncs(t *testing.T) {
	t.Parallel()

	a := &Authenticator{}
	a.SetExtractCredentialsFunc(func(_ *http.Request) (string, string, error) {
		return testUser1, testUser1p, nil
	})
	a.AddUser(testUser1, bcryptHash(testUser1p))

	u, p, err := a.ExtractCredentials(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || u != testUser1 || p != testUser1p {
		t.Fatalf("ExtractCredentials = (%q, %q, %v)", u, p, err)
	}

	a.SetSetCredentialsFunc(func(r *http.Request, user, credential string) error {
		r.Header.Set("X-Auth-User", user)
		r.Header.Set("X-Auth-Pass", credential)
		return nil
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := a.SetCredentials(req, testUser1, testUser1p); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if req.Header.Get("X-Auth-User") != testUser1 {
		t.Fatalf("X-Auth-User = %q", req.Header.Get("X-Auth-User"))
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	if err := (&Authenticator{}).SetCredentials(req, testUser1, testUser1p); err != nil {
		t.Fatalf("SetCredentials default: %v", err)
	}
	u, p, ok := req.BasicAuth()
	if !ok || u != testUser1 || p != testUser1p {
		t.Fatalf("BasicAuth = (%q, %q, %v)", u, p, ok)
	}
}

func TestCloneAndRegistryEntry(t *testing.T) {
	t.Parallel()

	a := &Authenticator{realm: "realm"}
	if _, ok := a.Clone().(*Authenticator); !ok {
		t.Fatal("Clone should return *Authenticator")
	}
	if RegistryEntry().Provider != ID {
		t.Fatalf("RegistryEntry provider = %q", RegistryEntry().Provider)
	}
	if _, err := New(map[string]any{"options": authopt.New()}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestProxyPreserveAndObserveOnlyFlags(t *testing.T) {
	t.Parallel()

	a := &Authenticator{proxyPreserve: true}
	if !a.ProxyPreserve() {
		t.Fatal("expected ProxyPreserve true")
	}
	a.SetObserveOnly(true)
	if !a.IsObserveOnly() {
		t.Fatal("expected observe-only true")
	}
}
