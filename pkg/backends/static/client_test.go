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

package static

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
)

func testBackendOptions(root string) *bo.Options {
	o := bo.New()
	o.Provider = providers.Static
	o.Static = testOptions(root)
	o.Static.FileserverCache.RevalidationInterval = 10 * 1000 * 1000 * 60 * 60 // 10h
	return o
}

func TestNewClient(t *testing.T) {
	if _, err := NewClient("test", nil, nil, nil, nil, nil); !errors.Is(err, ErrMissingOptions) {
		t.Errorf("expected ErrMissingOptions for nil options, got %v", err)
	}
	if _, err := NewClient("test", bo.New(), nil, nil, nil, nil); !errors.Is(err, ErrMissingOptions) {
		t.Errorf("expected ErrMissingOptions for a missing static block, got %v", err)
	}
	o := testBackendOptions(filepath.Join(t.TempDir(), "missing"))
	if _, err := NewClient("test", o, nil, nil, nil, nil); err == nil {
		t.Error("expected an error for a missing root")
	}
	b, err := NewClient("test", testBackendOptions(newTestSite(t)), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "test" {
		t.Errorf("expected client named test, got %s", b.Name())
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	dpc := (&Client{}).DefaultPathConfigs(nil)
	if len(dpc) != 1 {
		t.Fatalf("expected 1 default path, got %d", len(dpc))
	}
	if dpc[0].Path != "/" || dpc[0].HandlerName != providers.Static ||
		len(dpc[0].Methods) != len(methods.AllHTTPMethods()) {
		t.Error("expected a catch-all path routed to the static handler")
	}
}

func TestClientHandlerComposition(t *testing.T) {
	o := testBackendOptions(newTestSite(t))
	o.Static.DirectoryListing = true
	o.Static.ResponseHeaders = map[string]string{"X-Test": "1"}
	b, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := b.Handlers()[providers.Static]
	if !ok {
		t.Fatal("expected a registered static handler")
	}
	resp := get(t, h, http.MethodGet, "/empty/")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Test") != "1" {
		t.Errorf("expected a listing carrying the configured header, got %d", resp.StatusCode)
	}
}

func TestStartAndStopClients(t *testing.T) {
	b, err := NewClient("test", testBackendOptions(newTestSite(t)), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	other, err := backends.New("other", bo.New(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := backends.Backends{"test": c, "other": other}
	StartClients(clients)
	get(t, c.handler, http.MethodGet, "/")
	// the store is made away from the request
	c.server.stores.Wait()
	if c.server.cache.count() != 1 {
		t.Error("expected a started client to hold the requested file")
	}
	StopClients(clients)
	if c.server.cache.active.Load() || c.server.cache.count() != 0 {
		t.Error("expected a stopped client to hold nothing")
	}
	// both are safe to repeat, as a reload may stop clients that never started
	StopClients(clients)
	StartClients(clients)
	StartClients(clients)
	StopClients(clients)
}
