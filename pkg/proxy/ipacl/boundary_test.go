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

package ipacl

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/trickstercache/trickster/v2/pkg/"

// The configuration loader imports this package, and the relay's tests import
// the loader. An import of the relay, the backends or the controller from here
// is a cycle that go test reports and go build does not. The stream admission
// lives in a separate package for that reason; this one must not reach it,
// including pkg/proxy/l4/flow.

func forbidden(name string) bool {
	for _, bad := range []string{module + "backends", module + "kube", module + "proxy/l4"} {
		if name == bad || strings.HasPrefix(name, bad+"/") {
			return true
		}
	}
	return false
}

func TestForbiddenImports(t *testing.T) {
	for name, want := range map[string]bool{
		module + "proxy/l4":             true,
		module + "proxy/l4/flow":        true,
		module + "proxy/l4/observe":     true,
		module + "backends":             true,
		module + "backends/alb/options": true,
		module + "kube/gateway/compile": true,
		module + "proxy/clientip":       false,
		module + "config/reserved":      false,
		"net/netip":                     false,
	} {
		if got := forbidden(name); got != want {
			t.Errorf("forbidden(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestImportBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if forbidden(name) {
				t.Errorf("%s imports %s: the access list is compiled from configuration "+
					"and must not import the relay, the backends or the controller", e.Name(), name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}
