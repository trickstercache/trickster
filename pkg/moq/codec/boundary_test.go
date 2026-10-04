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

package codec

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestImportBoundary(t *testing.T) {
	// the wire format layers stay free of transports and of the rest of Trickster, so they can be tested and
	// reused alone: they may import the standard library and each other, and nothing else
	const root = "github.com/trickstercache/trickster/v2/pkg/moq/"
	allowed := []string{root + "wire", root + "message", root + "codec"}
	fset := token.NewFileSet()
	var files int
	for _, dir := range []string{"../wire", "../message", "."} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
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
				first, _, _ := strings.Cut(name, "/")
				if !strings.Contains(first, ".") {
					continue
				}
				ok := false
				for _, a := range allowed {
					ok = ok || name == a
				}
				if !ok {
					t.Errorf("%s imports %s: only the standard library and the moq wire layers are allowed", path, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}
