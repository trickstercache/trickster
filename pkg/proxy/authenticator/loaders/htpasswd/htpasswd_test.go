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

package htpasswd

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func bcryptHash(pass string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(h)
}

func TestLoadHtpasswdBcrypt(t *testing.T) {
	// Invalid file
	_, err := LoadHtpasswdBcrypt("/no/such/file")
	if err == nil {
		t.Error("expected error for missing file")
	}
	tempDir := t.TempDir()

	// Write a temp .htpasswd file
	htpasswd1 := filepath.Join(tempDir, "htpasswd")
	f, err := os.Create(htpasswd1)
	if err != nil {
		t.Fatalf("failed to create temp htpasswd: %v", err)
	}
	defer os.Remove(f.Name())
	users := []string{
		"foo:" + bcryptHash("bar"),
		"# comment",
		"badline",
		"baz:" + bcryptHash("quux"),
	}
	for _, l := range users {
		f.WriteString(l + "\n")
	}
	f.Close()

	m, err := LoadHtpasswdBcrypt(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Collect(maps.Keys(m))
	if !slices.Contains(keys, "foo") || !slices.Contains(keys, "baz") {
		t.Errorf("missing users from htpasswd file: %#v", maps.Keys(m))
	}
}
