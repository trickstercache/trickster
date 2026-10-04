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

package geo

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/trickstercache/trickster/v2/pkg/"

var configGraph = []string{ // the packages pkg/config imports, relative to this one
	".",
	"locator/providers",
	"locator/options",
	"locator/mmdb/options",
	"locator/geofeed/options",
	"locator/geofeed/feed",
	"locator/header/options",
	"acl/options",
}

func forbidden(name string) bool {
	// no package pkg/config imports may import pkg/config (a cycle), the relay (whose tests reach it
	// through pkg/config) or a locator implementation
	if name == module+"config/reserved" {
		return false
	}
	for _, bad := range []string{module + "config", module + "proxy/l4"} {
		if name == bad || strings.HasPrefix(name, bad+"/") {
			return true
		}
	}
	for _, bad := range []string{
		"proxy/geo/acl", "proxy/geo/acl/handler", "proxy/geo/acl/stream", "proxy/geo/locator/registry",
		"proxy/geo/locator/filesource", "proxy/geo/locator/mmdb", "proxy/geo/locator/geofeed",
		"proxy/geo/locator/header", "watchers/filesystem",
	} {
		if name == module+bad {
			return true
		}
	}
	return strings.HasPrefix(name, "github.com/oschwald/maxminddb-golang")
}

func TestForbiddenImports(t *testing.T) {
	for name, want := range map[string]bool{
		module + "config":                         true,
		module + "config/validate":                true,
		module + "config/reserved":                false,
		module + "proxy/l4":                       true,
		module + "proxy/l4/flow":                  true,
		module + "proxy/geo/acl":                  true,
		module + "proxy/geo/acl/options":          false,
		module + "proxy/geo/locator/geofeed":      true,
		module + "proxy/geo/locator/geofeed/feed": false,
		module + "proxy/clientip":                 false,
		"github.com/oschwald/maxminddb-golang/v2": true,
		"net/http": false,
	} {
		if got := forbidden(name); got != want {
			t.Errorf("forbidden(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestImportBoundary(t *testing.T) {
	fset := token.NewFileSet()
	var files int
	for _, dir := range configGraph {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			files++
			for _, imp := range f.Imports {
				name, _ := strconv.Unquote(imp.Path.Value)
				if forbidden(name) {
					t.Errorf("%s imports %s, which no package that pkg/config imports may", path, name)
				}
			}
		}
	}
	if files < len(configGraph) {
		t.Fatalf("only %d source files were checked", files)
	}
}
