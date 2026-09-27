/*
 * Copyright 2026 The Trickster Authors
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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func TestLoadIPACLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trickster.yaml")
	body := `
backends:
  api:
    provider: prometheus
    origin_url: http://prom:9090
    ip_acl_name: office
    paths:
      - path: /public/
        ip_acl_name: none
      - path: /admin/
        ip_acl_name: office
listeners:
  default:
    ip_acl_name: office
ip_acls:
  office:
    allow: ["10.0.0.0/8"]
    deny: ["10.0.5.0/24"]
  partners:
    allow: ["192.0.2.0/24"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	if c.IPACLs["office"] == nil || c.IPACLs["partners"] == nil {
		t.Fatalf("ip_acls = %#v", c.IPACLs)
	}
	if len(c.IPACLs["office"].Allow) != 1 || c.IPACLs["office"].Allow[0] != "10.0.0.0/8" {
		t.Fatalf("office allow = %#v", c.IPACLs["office"].Allow)
	}
	if c.IPACLs["office"].Compiled != nil {
		t.Fatal("load compiled an access list; compilation belongs to validation")
	}
	if c.Backends["api"].IPACLName != "office" || c.Listeners["default"].IPACLName != "office" {
		t.Fatal("ip_acl_name was not decoded")
	}
	if c.Backends["api"].Paths[0].IPACLName != "none" || c.Backends["api"].Paths[1].IPACLName != "office" {
		t.Fatalf("path names = %#v", c.Backends["api"].Paths)
	}

	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte(`
backends:
  api:
    provider: prometheus
    origin_url: http://prom:9090
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load([]string{"-config", plain})
	if err != nil {
		t.Fatal(err)
	}
	if c.IPACLs != nil {
		t.Fatalf("omitted ip_acls = %#v", c.IPACLs)
	}
}

func TestCloneIPACLs(t *testing.T) {
	c := NewConfig()
	c.IPACLs = ipacl.Lookup{"office": {Allow: []string{"10.0.0.0/8"}}}
	if _, err := c.IPACLs.Validate(); err != nil {
		t.Fatal(err)
	}
	list := c.IPACLs["office"].Compiled
	c.Backends["default"].IPACLName = "office"
	c.Backends["default"].IPACL = list
	c.Backends["default"].Paths = append(c.Backends["default"].Paths, &po.Options{
		Path: "/admin/", IPACLName: "office", IPACL: list,
	})
	c.Listeners["default"].IPACLName = "office"
	c.Listeners["default"].IPACL = list

	cloned := c.Clone()
	if cloned.IPACLs["office"].Compiled != list ||
		cloned.Backends["default"].IPACL != list ||
		cloned.Backends["default"].Paths[0].IPACL != list ||
		cloned.Listeners["default"].IPACL != list {
		t.Fatal("clone copied the compiled list")
	}
	cloned.IPACLs["office"].Allow[0] = "192.0.2.0/24"
	if c.IPACLs["office"].Allow[0] != "10.0.0.0/8" {
		t.Fatal("clone shares the allow slice")
	}
}
