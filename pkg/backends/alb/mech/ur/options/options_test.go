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
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"gopkg.in/yaml.v2"
)

func TestNew(t *testing.T) {
	t.Parallel()

	o := New()
	if o.NoRouteStatusCode != http.StatusUnauthorized {
		t.Fatalf("NoRouteStatusCode = %d, want 401", o.NoRouteStatusCode)
	}
}

func TestInitialize(t *testing.T) {
	t.Parallel()

	o := &Options{}
	if err := o.Initialize("edge-alb"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if o.NoRouteStatusCode != http.StatusUnauthorized {
		t.Fatalf("NoRouteStatusCode = %d, want default 401", o.NoRouteStatusCode)
	}

	o = &Options{NoRouteStatusCode: http.StatusNotFound}
	if err := o.Initialize("edge-alb"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if o.NoRouteStatusCode != http.StatusNotFound {
		t.Fatalf("NoRouteStatusCode = %d, want 404", o.NoRouteStatusCode)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	backendTypes := map[string]string{
		"tenant-a": providers.ReverseProxyShort,
	}

	o := New()
	o.DefaultBackend = "tenant-a"
	if err := o.Initialize("edge"); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(backendTypes); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	o = New()
	o.DefaultBackend = "missing"
	if err := o.Initialize("edge"); err != nil {
		t.Fatal(err)
	}
	err := o.Validate(backendTypes)
	if err == nil {
		t.Fatal("expected invalid default backend error")
	}
	var ie *InvalidUserRouterOptionsError
	if !errors.As(err, &ie) {
		t.Fatalf("Validate() = %T, want InvalidUserRouterOptionsError", err)
	}

	o = New()
	o.Users = UserMappingOptionsByUser{
		"alice": {ToBackend: "missing"},
	}
	if err := o.Initialize("edge"); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(backendTypes); err == nil {
		t.Fatal("expected invalid user backend error")
	}

	o = New()
	o.NoRouteStatusCode = 99
	if err := o.Initialize("edge"); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(backendTypes); err == nil {
		t.Fatal("expected invalid no_route_status_code error")
	}
}

func TestClone(t *testing.T) {
	t.Parallel()

	o := New()
	o.DefaultBackend = "tenant-a"
	o.Users = UserMappingOptionsByUser{
		"alice": {ToBackend: "tenant-a", ToUser: "alice"},
	}
	cl := o.Clone()
	if cl == o {
		t.Fatal("Clone returned same pointer")
	}
	if cl.Users["alice"] == o.Users["alice"] {
		t.Fatal("expected cloned user mapping")
	}
	cl.Users["alice"].ToUser = "bob"
	if o.Users["alice"].ToUser != "alice" {
		t.Fatal("clone should not share user mapping pointer")
	}
}

func TestNewErrInvalidUserRouterOptions(t *testing.T) {
	t.Parallel()

	err := NewErrInvalidUserRouterOptions("edge")
	var ie *InvalidUserRouterOptionsError
	if !errors.As(err, &ie) {
		t.Fatalf("error type = %T, want InvalidUserRouterOptionsError", err)
	}
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()

	const raw = `
backends:
  edge:
    user_router:
      default_backend: tenant-a
      no_route_status_code: 404
      users:
        alice:
          to_backend: tenant-a
          to_user: alice
`

	type backendDoc struct {
		Backends map[string]struct {
			UserRouter *Options `yaml:"user_router"`
		} `yaml:"backends"`
	}
	var doc backendDoc
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	o := doc.Backends["edge"].UserRouter
	if o == nil {
		t.Fatal("expected user router options")
	}
	if o.DefaultBackend != "tenant-a" {
		t.Fatalf("DefaultBackend = %q", o.DefaultBackend)
	}
	if o.NoRouteStatusCode != http.StatusNotFound {
		t.Fatalf("NoRouteStatusCode = %d, want 404", o.NoRouteStatusCode)
	}
	if o.Users["alice"].ToUser != "alice" {
		t.Fatalf("unexpected user mapping: %+v", o.Users["alice"])
	}
}
