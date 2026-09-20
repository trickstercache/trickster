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
package lb_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// the core must stay importable on its own: every file under this directory, tests included,
// may import the standard library and this package tree, and nothing else
func TestImportBoundary(t *testing.T) {
	const self = "github.com/trickstercache/trickster/v2/pkg/lb"
	fset := token.NewFileSet()
	var files int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if name == self || strings.HasPrefix(name, self+"/") {
				continue
			}
			// a standard library import path has no dot in its first element
			if first, _, _ := strings.Cut(name, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s: only the standard library is allowed under pkg/lb", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}
