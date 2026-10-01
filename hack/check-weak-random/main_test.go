package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func source(imports ...string) []byte {
	var sb strings.Builder
	sb.WriteString("package example\n\nimport (\n")
	for _, imp := range imports {
		sb.WriteString("\t\"" + imp + "\"\n")
	}
	sb.WriteString(")\n")
	return []byte(sb.String())
}

func TestViolations(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		imports []string
		want    int
	}{
		{"app math/rand", "pkg/util/middleware/mirror.go", []string{"math/rand"}, 1},
		{"app math/rand/v2", "pkg/util/middleware/mirror.go", []string{"fmt", "math/rand/v2"}, 1},
		{"test math/rand", "pkg/timeseries/dataset/dataset_test.go", []string{"math/rand"}, 1},
		{"tooling math/rand", "hack/load-test/main.go", []string{"math/rand/v2"}, 1},
		{"standalone hrw", "pkg/lb/hrw/hrw.go", []string{"math/rand/v2"}, 0},
		{"standalone p2c", "pkg/lb/p2c/p2c.go", []string{"math/rand/v2"}, 0},
		{"standalone rr", "pkg/lb/rr/rr.go", []string{"math/rand/v2"}, 0},
		{"standalone test helper", "pkg/lb/lbtest/lbtest.go", []string{"math/rand/v2"}, 0},
		{"standalone old rand", "pkg/lb/rr/rr.go", []string{"math/rand"}, 1},
		{"new standalone use", "pkg/lb/rr/new.go", []string{"math/rand/v2"}, 1},
		{"other standalone strategy", "pkg/lb/lc/lc.go", []string{"math/rand/v2"}, 1},
		{"app compat", "pkg/util/middleware/mirror.go", []string{compatPath}, 0},
		{"app weaktest", "pkg/util/middleware/mirror.go", []string{weaktestPath}, 1},
		{"main weak", "cmd/trickster/main.go", []string{"github.com/trickstercache/trickster/v2/pkg/util/weak"}, 0},
		{"test compat", "pkg/util/middleware/mirror_test.go", []string{compatPath}, 1},
		{"test weaktest", "pkg/util/middleware/mirror_test.go", []string{weaktestPath}, 0},
		{"testutil compat", "pkg/testutil/albpool/albpool.go", []string{compatPath}, 1},
		{"testutil weaktest", "pkg/testutil/albpool/albpool.go", []string{weaktestPath}, 0},
		{"helper package weaktest", "pkg/lb/lbtest/lbtest.go", []string{weaktestPath}, 0},
		{"helper package compat", "pkg/lb/lbtest/lbtest.go", []string{compatPath}, 1},
		{"integration weaktest", "integration/main_test.go", []string{weaktestPath}, 0},
		{"integration compat", "integration/harness.go", []string{compatPath}, 1},
		{"weak packages", "pkg/util/weak/compat/compat.go", []string{"math/rand/v2"}, 0},
		{"weak tests", "pkg/util/weak/weak_test.go", []string{compatPath, weaktestPath}, 0},
		{"everything wrong", "pkg/proxy/proxy.go", []string{"math/rand", weaktestPath, "math/rand/v2"}, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := violations(test.file, source(test.imports...))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.want {
				t.Errorf("got %d violations %q, want %d", len(got), got, test.want)
			}
		})
	}
	if _, err := violations("pkg/x.go", []byte("not go")); err == nil {
		t.Error("expected a parse error")
	}
}

func TestCheck(t *testing.T) {
	root := t.TempDir()
	write := func(file string, imports ...string) {
		p := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, source(imports...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg/app/app.go", compatPath)
	write("pkg/app/app_test.go", weaktestPath)
	write("pkg/app/testdata/ignored.go", "math/rand")
	write("pkg/vendor/ignored.go", "math/rand")
	write("pkg/app/README.md")
	problems, err := check(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %q", problems)
	}

	write("cmd/bad/main.go", weaktestPath)
	problems, err = check(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "cmd/bad/main.go:") {
		t.Fatalf("unexpected problems: %q", problems)
	}

	write("pkg/broken.go")
	if err := os.WriteFile(filepath.Join(root, "pkg", "broken.go"), []byte("not go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := check(root); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestRepositoryConforms(t *testing.T) {
	problems, err := check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}
