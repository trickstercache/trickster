// Command check-imports reports Go files whose imports are not grouped as:
//
//	standard library
//
//	github.com/trickstercache/trickster/v2 packages
//
//	external packages
//
// Run it from the root of the repository:
//
//	go run ./hack/check-imports
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const localImportPrefix = "github.com/trickstercache/"

type importKind int

const (
	standardImport importKind = iota
	localImport
	externalImport
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		if _, err2 := fmt.Fprintln(os.Stderr, err); err2 != nil {
			fmt.Printf("failed to print error to STDERR: %s (%s)", err, err2)
		}
		os.Exit(2)
	}

	found := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_gen.go") ||
			strings.Contains(path, "/vendor/") {
			return nil
		}

		conforms, err := importsConform(path)
		if err != nil {
			return err
		}
		if !conforms {
			relativePath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !found {
				fmt.Print("Incorrect import ordering in the files below.\n" +
					"Use 3 distinct sections: standard/builtin, github.com/trickstercache/*, external\n\n" +
					"Example:" + `

import (
    "fmt"
    "os"

    "github.com/trickstercache/trickster/v2/pkg/cache"
    "github.com/trickstercache/trickster/v2/pkg/proxy"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

--------------------------------------------------------------------

`)
			}
			fmt.Println(relativePath)
			found = true
		}
		return nil
	})
	if err != nil {
		if _, err2 := fmt.Fprintln(os.Stderr, err); err2 != nil {
			fmt.Printf("failed to print error to STDERR: %s (%s)", err, err2)
		}
		os.Exit(2)
	}
	if found {
		fmt.Println()
		os.Exit(1)
	}
	fmt.Print("\n\033[1;32m✓\033[0m All code files have the correct import structuring.\n\n")
}

func importsConform(path string) (bool, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.ImportsOnly)
	if err != nil {
		return false, err
	}
	if len(file.Imports) < 2 {
		return true, nil
	}

	lines := sourceLines(source)
	previousKind := kindOf(file.Imports[0])
	previousLine := fileSet.Position(file.Imports[0].End()).Line
	for _, spec := range file.Imports[1:] {
		currentKind := kindOf(spec)
		currentLine := fileSet.Position(spec.Pos()).Line
		blankLines := countBlankLines(lines, previousLine, currentLine)

		if currentKind < previousKind ||
			(currentKind == previousKind && blankLines != 0) ||
			(currentKind != previousKind && blankLines != 1) {
			return false, nil
		}

		previousKind = currentKind
		previousLine = fileSet.Position(spec.End()).Line
	}

	return true, nil
}

func kindOf(spec *ast.ImportSpec) importKind {
	importPath, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return externalImport
	}
	if strings.HasPrefix(importPath, localImportPrefix) {
		return localImport
	}

	firstComponent, _, _ := strings.Cut(importPath, "/")
	if !strings.Contains(firstComponent, ".") {
		return standardImport
	}
	return externalImport
}

func sourceLines(source []byte) [][]byte {
	return bytes.Split(source, []byte("\n"))
}

func countBlankLines(lines [][]byte, previousLine, currentLine int) int {
	count := 0
	for line := previousLine + 1; line < currentLine; line++ {
		if len(bytes.TrimSpace(lines[line-1])) == 0 {
			count++
		}
	}
	return count
}
