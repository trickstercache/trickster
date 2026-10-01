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

package greptimedb_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeveloperSeedStartupOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("developer Docker lifecycle script uses Linux paths")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for the developer lifecycle script")
	}
	script, err := os.ReadFile("../../hack/developer-seed-data.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, target, mode string
		wantFailure        bool
	}{
		{"running_seeders", "greptimedb", "active", false},
		{"no_running_seeders", "greptimedb", "idle", false},
		{"startup_failure", "greptimedb", "failed", true},
		{"wait_failure", "greptimedb", "wait-error", true},
		{"listing_failure", "greptimedb", "list-error", true},
		{"graphite_only", "graphite", "active", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"hack", "bin", "docs/developer/environment"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name string, body []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), body, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write("hack/developer-seed-data.sh", bytes.ReplaceAll(script, []byte("\r\n"), []byte("\n")))
			write("bin/docker", []byte(`#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$*" in
  'compose ps -q --status running '*)
    test "$MOCK_MODE" != list-error || exit 9
    test "$MOCK_MODE" != idle || exit 0
    printf 'startup-one\nstartup-two\n'
    ;;
  'wait startup-one') printf '0\n' ;;
  'wait startup-two')
    test "$MOCK_MODE" != wait-error || exit 8
    if test "$MOCK_MODE" = failed; then printf '7\n'; else printf '0\n'; fi
    ;;
esac
`))
			logPath := filepath.Join(root, "docker.log")
			cmd := exec.Command(bash, filepath.Join(root, "hack/developer-seed-data.sh"))
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"DOCKER_LOG="+logPath, "MOCK_MODE="+tt.mode, "SEED_TARGET="+tt.target)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantFailure {
				t.Fatalf("error = %v, want failure %v; output: %s", err, tt.wantFailure, output)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(data)
			lines := strings.Split(strings.TrimSpace(log), "\n")
			if !strings.HasPrefix(lines[0], "compose ps -q --status running ") {
				t.Fatalf("first command must inspect startup seeders: %s", log)
			}
			if tt.target == "graphite" {
				if lines[0] != "compose ps -q --status running graphite_seed" || strings.Contains(log, "run --rm seed_data_generate") {
					t.Fatalf("graphite-only reload touched the trips fixture: %s", log)
				}
			} else {
				for _, service := range []string{"seed_data_generate", "clickhouse_seed", "mysql_seed", "timescaledb_seed", "greptimedb_seed", "druid_seed"} {
					if !strings.Contains(lines[0], service) {
						t.Fatalf("shared fixture consumer %s was not checked: %s", service, log)
					}
				}
			}
			if tt.wantFailure {
				if strings.Contains(log, "compose up") || strings.Contains(log, "compose run") || strings.Contains(log, "compose stop") {
					t.Fatalf("startup failure must prevent mutations: %s", log)
				}
				return
			}
			if tt.mode == "idle" {
				if strings.Contains(log, "wait startup-") {
					t.Fatalf("wait called without running seeders: %s", log)
				}
			} else if len(lines) < 4 || lines[1] != "wait startup-one" || lines[2] != "wait startup-two" {
				t.Fatalf("mutations preceded startup completion: %s", log)
			}
		})
	}
}
