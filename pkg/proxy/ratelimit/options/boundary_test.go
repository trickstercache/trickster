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

package options

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/trickstercache/trickster/v2/pkg/"

// configuration imports this package, so an import of the relay here is a cycle only go test reports

func forbidden(name string) bool {
	if name == module+"config/reserved" {
		return false
	}
	for _, bad := range []string{
		module + "backends",
		module + "kube",
		module + "proxy/l4",
		module + "config",
		module + "daemon",
	} {
		if name == bad || strings.HasPrefix(name, bad+"/") {
			return true
		}
	}
	return false
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
				t.Errorf("%s imports %s", e.Name(), name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}
