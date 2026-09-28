// Command check-weak-random fails when non-cryptographic randomness crosses the
// line between application code and test code; see package pkg/util/weak.
package main

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

const (
	weakDir      = "pkg/util/weak/"
	compatPath   = "github.com/trickstercache/trickster/v2/pkg/util/weak/compat"
	weaktestPath = "github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

var roots = []string{"cmd", "pkg", "integration", "examples", "hack"}

func main() {
	problems, err := check(".")
	if err != nil {
		if _, err2 := fmt.Fprintln(os.Stderr, err); err2 != nil {
			fmt.Printf("failed to print error to STDERR: %s (%s)", err, err2)
		}
		os.Exit(2)
	}
	if len(problems) > 0 {
		fmt.Print("Non-cryptographic randomness must go through pkg/util/weak/compat in the\n" +
			"application and pkg/util/weak/weaktest in tests and tooling:\n\n")
		for _, p := range problems {
			fmt.Println(p)
		}
		fmt.Println()
		os.Exit(1)
	}
	fmt.Print("\nWeak randomness uses the reviewed wrappers and standalone-core exceptions.\n\n")
}

func check(repoRoot string) ([]string, error) {
	r, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	fsys := r.FS()
	var problems []string
	for _, root := range roots {
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if path.Ext(p) != ".go" {
				return nil
			}
			src, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			found, err := violations(p, src)
			problems = append(problems, found...)
			return err
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return problems, nil
}

func violations(file string, src []byte) ([]string, error) {
	// the weak packages and their tests are the one place that may use both sides
	if strings.HasPrefix(file, weakDir) {
		return nil, nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	app := isApplication(file)
	var out []string
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		switch {
		case imp == "math/rand/v2" && standaloneRandom(file):
			// These pre-existing uses keep the load-balancing core independently importable.
		case imp == "math/rand" || imp == "math/rand/v2":
			out = append(out, file+": imports "+imp+"; use pkg/util/weak/compat or pkg/util/weak/weaktest")
		case imp == compatPath && !app:
			out = append(out, file+": test or tooling code imports pkg/util/weak/compat; use pkg/util/weak/weaktest")
		case imp == weaktestPath && app:
			out = append(out, file+": application code imports pkg/util/weak/weaktest; use pkg/util/weak/compat")
		}
	}
	return out, nil
}

func standaloneRandom(file string) bool {
	switch file {
	case "pkg/lb/hrw/hrw.go", "pkg/lb/p2c/p2c.go", "pkg/lb/rr/rr.go", "pkg/lb/lbtest/lbtest.go":
		return true
	}
	return false
}

func isApplication(file string) bool {
	// tests, test-support packages and everything outside cmd/ and pkg/ are not the application
	if strings.HasSuffix(file, "_test.go") || strings.Contains(file, "/testutil/") {
		return false
	}
	if !strings.HasPrefix(file, "cmd/") && !strings.HasPrefix(file, "pkg/") {
		return false
	}
	return !strings.HasSuffix(path.Base(path.Dir(file)), "test")
}
