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

package rpc

import (
	"os"
	"path/filepath"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

func TestNew(t *testing.T) {
	h, err := New("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestNewWithOptions(t *testing.T) {
	o := bo.New()
	o.Name = "rpc-test"
	c := co.New()
	c.Name = "rpc-cache"
	h, err := NewWithOptions("http://127.0.0.1:8080/app", o, c)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestNewBadURL(t *testing.T) {
	_, err := New("http://[::1")
	if err == nil {
		t.Fatal("expected URL parse error")
	}
}

func TestNewCacheConnectError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := co.New()
	c.Name = "bad-fs"
	c.Provider = providers.Filesystem
	c.Filesystem.CachePath = f

	_, err := NewWithOptions("http://127.0.0.1:8080", nil, c)
	if err == nil {
		t.Fatal("expected cache connect error")
	}
}

func TestNewClientError(t *testing.T) {
	o := bo.New()
	o.Name = "bad-tls"
	o.TLS = &tlsopts.Options{
		ClientCertPath: "/no/such/cert.pem",
		ClientKeyPath:  "/no/such/key.pem",
	}
	_, err := NewWithOptions("http://127.0.0.1:8080", o, nil)
	if err == nil {
		t.Fatal("expected client construction error")
	}
}
