/*
 * Copyright 2018 The Trickster Authors
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

// Package golden holds JSON-fixture helpers for tests that maintain
// testdata/ goldens. The package owns the shared -update flag so
// multiple consumers route through one switch.
package golden

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Update is the shared -update flag for golden regeneration.
var Update = flag.Bool("update", false, "rewrite testdata/*.json golden fixtures from current test output")

// LoadJSON reads testdata/<name>.json relative to the calling test's
// package directory and decodes it into out.
func LoadJSON(t testing.TB, name string, out any) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal golden %s: %v", path, err)
	}
}

// WriteJSON marshals v with MarshalIndent and writes it to
// testdata/<name>.json, creating the directory if necessary.
func WriteJSON(t testing.TB, name string, v any) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
