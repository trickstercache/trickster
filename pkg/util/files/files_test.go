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

package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileExistsAndReadable(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "does-not-exist")
	if FileExistsAndReadable(missing) {
		t.Errorf("expected false for missing file %q", missing)
	}

	path := filepath.Join(dir, "readable.txt")
	if err := os.WriteFile(path, []byte("trickster"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	if !FileExistsAndReadable(path) {
		t.Errorf("expected true for readable file %q", path)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove temp file: %v", err)
	}

	if FileExistsAndReadable(path) {
		t.Errorf("expected false for deleted file %q", path)
	}
}
