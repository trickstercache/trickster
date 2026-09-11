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

package influxdb

import (
	"os"
	"path/filepath"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

func TestNewAccelerator(t *testing.T) {
	h, err := NewAccelerator("http://127.0.0.1:8086")
	if err != nil {
		t.Fatalf("NewAccelerator: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestNewAcceleratorWithOptions(t *testing.T) {
	o := bo.New()
	o.Name = "influx-test"
	c := co.New()
	c.Name = "influx-cache"
	h, err := NewAcceleratorWithOptions("http://127.0.0.1:8086/influx", o, c)
	if err != nil {
		t.Fatalf("NewAcceleratorWithOptions: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestNewAcceleratorBadURL(t *testing.T) {
	_, err := NewAccelerator("http://[::1")
	if err == nil {
		t.Fatal("expected URL parse error")
	}
}

func TestNewAcceleratorCacheConnectError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := co.New()
	c.Name = "bad-fs"
	c.Provider = providers.Filesystem
	c.Filesystem.CachePath = f

	_, err := NewAcceleratorWithOptions("http://127.0.0.1:8086", nil, c)
	if err == nil {
		t.Fatal("expected cache connect error")
	}
}

func TestNewAcceleratorClientError(t *testing.T) {
	o := bo.New()
	o.Name = "bad-tls"
	o.TLS = &tlsopts.Options{
		ClientCertPath: "/no/such/cert.pem",
		ClientKeyPath:  "/no/such/key.pem",
	}
	_, err := NewAcceleratorWithOptions("http://127.0.0.1:8086", o, nil)
	if err == nil {
		t.Fatal("expected client construction error")
	}
}
