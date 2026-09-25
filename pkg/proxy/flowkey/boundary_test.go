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
package flowkey

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/trickstercache/trickster/v2/pkg/"

// flow keys serve every feature that stands in front of a backend, so this package may not
// import the backends or the controller that use it. Nor may it import the relay: the relay's
// tests reach this package through the config tree, so that import is a cycle; the relay's
// flow type stands apart in its own package for exactly that reason, and that one is allowed

func forbidden(name string) bool {
	if name == module+"proxy/l4/flow" {
		return false
	}
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
		module + "proxy/l4/observe":     true,
		module + "proxy/l4/options":     true,
		module + "proxy/l4/flow":        false,
		module + "backends":             true,
		module + "backends/alb/options": true,
		module + "kube/gateway/compile": true,
		module + "lb":                   false,
		module + "proxy/context":        false,
		module + "proxy/headers":        false,
		"net/http":                      false,
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
			name, _ := strconv.Unquote(imp.Path.Value)
			if forbidden(name) {
				t.Errorf("%s imports %s: flow keys are read for the backends, the controller and the relay, "+
					"never from them", e.Name(), name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}
