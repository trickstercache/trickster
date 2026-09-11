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

package authenticator

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	pkgerrors "github.com/trickstercache/trickster/v2/pkg/errors"
	ae "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/errors"
	authopt "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
)

func TestRegistryEntry(t *testing.T) {
	t.Parallel()
	e := RegistryEntry()
	if e.Provider != ID {
		t.Errorf("unexpected provider %q", e.Provider)
	}
	if e.Provider != providers.ClickHouse {
		t.Errorf("expected clickhouse provider, got %q", e.Provider)
	}
	if e.New == nil {
		t.Fatal("expected New constructor")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	a, err := New(map[string]any{"options": authopt.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil authenticator")
	}

	_, err = New(nil)
	if !errors.Is(err, pkgerrors.ErrInvalidOptions) {
		t.Fatalf("expected ErrInvalidOptions, got %v", err)
	}
}

func TestSetAndExtractCredentialsQueryParams(t *testing.T) {
	t.Parallel()
	auth, err := New(map[string]any{"options": authopt.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example/?user=old&password=oldpass", nil)
	if err := auth.SetCredentials(req, "alice", "secret"); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	q := req.URL.Query()
	if q.Get("user") != "alice" || q.Get("password") != "secret" {
		t.Errorf("query creds = user=%q password=%q", q.Get("user"), q.Get("password"))
	}

	u, p, err := auth.ExtractCredentials(req)
	if err != nil {
		t.Fatalf("ExtractCredentials: %v", err)
	}
	if u != "alice" || p != "secret" {
		t.Errorf("extracted (%q, %q)", u, p)
	}
}

func TestSetAndExtractCredentialsBasicAuth(t *testing.T) {
	t.Parallel()
	auth, err := New(map[string]any{"options": authopt.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
	if err := auth.SetCredentials(req, "bob", "token"); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	u, p, ok := req.BasicAuth()
	if !ok || u != "bob" || p != "token" {
		t.Errorf("basic auth = (%q, %q, %v)", u, p, ok)
	}

	u, p, err = auth.ExtractCredentials(req)
	if err != nil {
		t.Fatalf("ExtractCredentials: %v", err)
	}
	if u != "bob" || p != "token" {
		t.Errorf("extracted (%q, %q)", u, p)
	}
}

func TestExtractCredentialsMissing(t *testing.T) {
	t.Parallel()
	auth, err := New(map[string]any{"options": authopt.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
	_, _, err = auth.ExtractCredentials(req)
	if !errors.Is(err, ae.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	// Only one of the query params present falls back to BasicAuth and fails.
	req = httptest.NewRequest(http.MethodGet, "http://example/?user=alice", nil)
	_, _, err = auth.ExtractCredentials(req)
	if !errors.Is(err, ae.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}
