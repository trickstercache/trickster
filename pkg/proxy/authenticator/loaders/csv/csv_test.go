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

package csv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func TestLoadCSVWithHeader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "users.csv")
	content := "username,password\n alice , secret \n bob,hash\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	users, err := LoadCSV(path, types.CSV)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if users["alice"] != "secret" || users["bob"] != "hash" {
		t.Fatalf("users = %+v", users)
	}
}

func TestLoadCSVNoHeader(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "users.csv")
	if err := os.WriteFile(path, []byte("alice,secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	users, err := LoadCSV(path, types.CSVNoHeader)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if users["alice"] != "secret" {
		t.Fatalf("users = %+v", users)
	}
}

func TestLoadCSVSkipsHeaderRow(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "users.csv")
	content := "username,password\nbob,hash\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	users, err := LoadCSV(path, types.CSV)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if len(users) != 1 || users["bob"] != "hash" {
		t.Fatalf("users = %+v", users)
	}
}

func TestLoadCSVOpenError(t *testing.T) {
	t.Parallel()

	_, err := LoadCSV(filepath.Join(t.TempDir(), "missing.csv"), types.CSV)
	if err == nil {
		t.Fatal("expected open error")
	}
}

func TestLoadCSVMalformedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(path, []byte("\"unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCSV(path, types.CSVNoHeader)
	if err == nil {
		t.Fatal("expected csv parse error")
	}
}
