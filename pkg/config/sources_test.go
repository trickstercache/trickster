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

package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const configSourceTestPrimary = `
main:
  server_name: "yes"
backends:
  primary:
    provider: prometheus
    origin_url: http://primary:9090
    proxy_only: yes
    hosts:
      - primary.example
      - old.example
frontend:
  listen_port: 8480
`

func writeConfigSourceTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeConfigSourceTestDirectory(t *testing.T) (string, string) {
	t.Helper()
	directoryPath := t.TempDir()
	configPath := filepath.Join(directoryPath, "trickster.yaml")
	includePath := filepath.Join(directoryPath, defaultConfigIncludeDirectory)
	if err := os.Mkdir(includePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfigSourceTestFile(t, configPath, configSourceTestPrimary)
	return configPath, includePath
}

func TestLoadConfigFileWithFragments(t *testing.T) {
	configPath, includePath := makeConfigSourceTestDirectory(t)
	writeConfigSourceTestFile(t, filepath.Join(includePath, "10-secondary.yaml"), `
backends:
  secondary:
    provider: prometheus
    origin_url: http://secondary:9090
`)
	writeConfigSourceTestFile(t, filepath.Join(includePath, "20-primary.conf"), `
backends:
  primary:
    origin_url: http://primary-override:9090
    hosts:
      - replacement.example
frontend:
  listen_port: 9001
`)
	writeConfigSourceTestFile(t, filepath.Join(includePath, "30-frontend.yml"), `
frontend:
  listen_port: 9002
`)
	writeConfigSourceTestFile(t, filepath.Join(includePath, ".ignored.yaml"), "[[")
	writeConfigSourceTestFile(t, filepath.Join(includePath, "ignored.txt"), "[[")

	config, err := Load([]string{"-config", configPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Backends) != 2 {
		t.Fatalf("loaded backends = %d; want 2", len(config.Backends))
	}
	primary := config.Backends["primary"]
	if primary.Provider != "prometheus" || primary.OriginURL != "http://primary-override:9090" {
		t.Errorf("partial backend override lost fields: provider=%q origin_url=%q",
			primary.Provider, primary.OriginURL)
	}
	if !primary.ProxyOnly {
		t.Error("YAML 1.1 boolean value was not preserved through the merge")
	}
	if len(primary.Hosts) != 1 || primary.Hosts[0] != "replacement.example" {
		t.Errorf("hosts = %v; want replacement sequence", primary.Hosts)
	}
	if config.Main.ServerName != "yes" {
		t.Errorf("server name = %q; want %q", config.Main.ServerName, "yes")
	}
	if config.Frontend.ListenPort != 9002 {
		t.Errorf("frontend port = %d; want lexical last value 9002", config.Frontend.ListenPort)
	}
	if config.ConfigFilePath() != configPath {
		t.Errorf("config path = %q; want %q", config.ConfigFilePath(), configPath)
	}
	wantPaths := []string{
		configPath,
		filepath.Join(includePath, "10-secondary.yaml"),
		filepath.Join(includePath, "20-primary.conf"),
		filepath.Join(includePath, "30-frontend.yml"),
	}
	if got := config.ConfigFilePaths(); !slices.Equal(got, wantPaths) {
		t.Errorf("config paths = %v; want %v", got, wantPaths)
	}
	if config.Main.configSourcePlan.includeDirectoryPath != includePath {
		t.Errorf("include path = %q; want %q",
			config.Main.configSourcePlan.includeDirectoryPath, includePath)
	}
}

func TestLoadConfigDirectory(t *testing.T) {
	directoryPath := t.TempDir()
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, "10-primary.yaml"), configSourceTestPrimary)
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, "20-override.conf"), `
backends:
  primary:
    origin_url: http://directory-override:9090
`)
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, "30-listener.YML"), `
frontend:
  listen_port: 9010
`)
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, ".ignored.yaml"), "[[")

	config, err := Load([]string{"-config", directoryPath})
	if err != nil {
		t.Fatal(err)
	}
	primary := config.Backends["primary"]
	if primary.Provider != "prometheus" || primary.OriginURL != "http://directory-override:9090" {
		t.Errorf("directory merge lost fields: provider=%q origin_url=%q",
			primary.Provider, primary.OriginURL)
	}
	if config.Frontend.ListenPort != 9010 {
		t.Errorf("frontend port = %d; want 9010", config.Frontend.ListenPort)
	}
	if config.ConfigFilePath() != directoryPath {
		t.Errorf("config path = %q; want directory %q", config.ConfigFilePath(), directoryPath)
	}
}

func TestLoadConfigDirectoryWithYAMLAlias(t *testing.T) {
	directoryPath := t.TempDir()
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, "10-primary.yaml"), configSourceTestPrimary)
	writeConfigSourceTestFile(t, filepath.Join(directoryPath, "20-alias.yaml"), `
backends:
  primary: &backend_defaults
    timeout: 2m
  secondary:
    <<: *backend_defaults
    provider: prometheus
    origin_url: http://secondary:9090
`)

	config, err := Load([]string{"-config", directoryPath})
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(config.Backends["primary"].Timeout) != 2*time.Minute {
		t.Errorf("primary timeout = %s; want 2m", time.Duration(config.Backends["primary"].Timeout))
	}
	if time.Duration(config.Backends["secondary"].Timeout) != 2*time.Minute {
		t.Errorf("aliased timeout = %s; want 2m", time.Duration(config.Backends["secondary"].Timeout))
	}
}

func TestLoadConfigMissingPathError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := Load([]string{"-config", path})
	if err == nil {
		t.Fatal("expected an error for a missing config path")
	}
	var pathError *os.PathError
	if !errors.As(err, &pathError) {
		t.Fatalf("error = %T %v; want *os.PathError", err, err)
	}
	if pathError.Op != "open" {
		t.Errorf("error operation = %q; want %q", pathError.Op, "open")
	}
	if pathError.Path != path {
		t.Errorf("error path = %q; want %q", pathError.Path, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v; want os.ErrNotExist", err)
	}
}

func TestLoadConfigExplicitIncludeDirectory(t *testing.T) {
	directoryPath := t.TempDir()
	configPath := filepath.Join(directoryPath, "trickster.yaml")
	includePath := filepath.Join(directoryPath, "pieces")
	if err := os.Mkdir(includePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfigSourceTestFile(t, configPath, strings.Replace(configSourceTestPrimary,
		"main:\n", "main:\n  config_include_directory: pieces\n", 1))
	writeConfigSourceTestFile(t, filepath.Join(includePath, "frontend.yaml"), `
frontend:
  listen_port: 9123
`)

	config, err := Load([]string{"-config", configPath})
	if err != nil {
		t.Fatal(err)
	}
	if config.Main.ConfigIncludeDirectory != "pieces" {
		t.Errorf("include setting = %q; want %q", config.Main.ConfigIncludeDirectory, "pieces")
	}
	if config.Main.configSourcePlan.includeDirectoryPath != includePath {
		t.Errorf("include path = %q; want %q",
			config.Main.configSourcePlan.includeDirectoryPath, includePath)
	}
	if config.Frontend.ListenPort != 9123 {
		t.Errorf("frontend port = %d; want 9123", config.Frontend.ListenPort)
	}
}

func TestLoadConfigIncludeDirectoryFromRootMerge(t *testing.T) {
	directoryPath := t.TempDir()
	configPath := filepath.Join(directoryPath, "trickster.yaml")
	includePath := filepath.Join(directoryPath, "pieces")
	if err := os.Mkdir(includePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfigSourceTestFile(t, configPath, `
defaults: &defaults
  main:
    config_include_directory: pieces
<<: *defaults
backends:
  primary:
    provider: prometheus
    origin_url: http://primary:9090
`)
	writeConfigSourceTestFile(t, filepath.Join(includePath, "frontend.yaml"), `
frontend:
  listen_port: 9124
`)

	config, err := Load([]string{"-config", configPath})
	if err != nil {
		t.Fatal(err)
	}
	if config.Main.ConfigIncludeDirectory != "pieces" {
		t.Errorf("include setting = %q; want %q", config.Main.ConfigIncludeDirectory, "pieces")
	}
	if config.Frontend.ListenPort != 9124 {
		t.Errorf("frontend port = %d; want 9124", config.Frontend.ListenPort)
	}
}

func TestLoadConfigSourcesFailures(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T) string
		errorMatch string
	}{
		{
			name: "empty directory",
			prepare: func(t *testing.T) string {
				return t.TempDir()
			},
			errorMatch: "contains no supported config files",
		},
		{
			name: "missing explicit include directory",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "trickster.yaml")
				writeConfigSourceTestFile(t, path, strings.Replace(configSourceTestPrimary,
					"main:\n", "main:\n  config_include_directory: missing\n", 1))
				return path
			},
			errorMatch: "config include directory",
		},
		{
			name: "malformed fragment",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "broken.yaml"), "[[")
				return path
			},
			errorMatch: "broken.yaml",
		},
		{
			name: "multiple documents",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "multiple.yaml"), "frontend: {}\n---\nmetrics: {}\n")
				return path
			},
			errorMatch: "multiple YAML documents",
		},
		{
			name: "non-mapping document",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "sequence.yaml"), "- frontend\n")
				return path
			},
			errorMatch: "root must be a mapping",
		},
		{
			name: "duplicate key",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "duplicate.yaml"), "frontend:\n  listen_port: 1\n  listen_port: 2\n")
				return path
			},
			errorMatch: "listen_port\" is repeated",
		},
		{
			name: "cyclic alias",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "cycle.yaml"), "cycle: &cycle [*cycle]\n")
				return path
			},
			errorMatch: "YAML alias cycle",
		},
		{
			name: "fragment redirects discovery",
			prepare: func(t *testing.T) string {
				path, includePath := makeConfigSourceTestDirectory(t)
				writeConfigSourceTestFile(t, filepath.Join(includePath, "redirect.yaml"), "main:\n  config_include_directory: elsewhere\n")
				return path
			},
			errorMatch: "cannot set main.config_include_directory",
		},
		{
			name: "directory source redirects discovery",
			prepare: func(t *testing.T) string {
				directoryPath := t.TempDir()
				writeConfigSourceTestFile(t, filepath.Join(directoryPath, "config.yaml"), strings.Replace(configSourceTestPrimary,
					"main:\n", "main:\n  config_include_directory: elsewhere\n", 1))
				return directoryPath
			},
			errorMatch: "cannot set main.config_include_directory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load([]string{"-config", test.prepare(t)})
			if err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("error = %v; want text %q", err, test.errorMatch)
			}
		})
	}
}

func TestLoadSingleConfigUsesStrictParsing(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		errorMatch string
	}{
		{
			name:       "multiple documents",
			contents:   "frontend: {}\n---\nmetrics: {}\n",
			errorMatch: "multiple YAML documents",
		},
		{
			name:       "non-mapping document",
			contents:   "- frontend\n",
			errorMatch: "root must be a mapping",
		},
		{
			name:       "duplicate key",
			contents:   "frontend:\n  listen_port: 1\n  listen_port: 2\n",
			errorMatch: "listen_port",
		},
		{
			name:       "cyclic alias",
			contents:   "cycle: &cycle [*cycle]\n",
			errorMatch: "YAML alias cycle",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trickster.yaml")
			writeConfigSourceTestFile(t, path, test.contents)
			_, err := Load([]string{"-config", path})
			if err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("error = %v; want text %q", err, test.errorMatch)
			}
		})
	}
}

func TestConfigSourceChangesAreStale(t *testing.T) {
	t.Run("file and include directory", func(t *testing.T) {
		configPath, includePath := makeConfigSourceTestDirectory(t)
		fragmentPath := filepath.Join(includePath, "10-frontend.yaml")
		writeConfigSourceTestFile(t, fragmentPath, "frontend:\n  listen_port: 9001\n")

		config, err := Load([]string{"-config", configPath})
		if err != nil {
			t.Fatal(err)
		}
		config.MgmtConfig.ReloadRateLimit = 0
		if config.CheckAndMarkReloadInProgress() {
			t.Fatal("freshly loaded config reported stale")
		}
		if config.HasConfigChanged() {
			t.Fatal("freshly loaded config sources reported changed")
		}

		info, err := os.Stat(fragmentPath)
		if err != nil {
			t.Fatal(err)
		}
		writeConfigSourceTestFile(t, fragmentPath, "frontend:\n  listen_port: 9002\n")
		if err := os.Chtimes(fragmentPath, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if !config.HasConfigChanged() || !config.HasConfigChanged() {
			t.Error("same-timestamp content change was not detected without marking")
		}
		if !config.CheckAndMarkReloadInProgress() {
			t.Error("same-timestamp content change did not make config stale")
		}
		if config.CheckAndMarkReloadInProgress() {
			t.Error("unchanged source reported stale after being marked")
		}

		writeConfigSourceTestFile(t, filepath.Join(includePath, "ignored.txt"), "ignored")
		if config.HasConfigChanged() {
			t.Error("unsupported file was detected as a config source change")
		}
		if config.CheckAndMarkReloadInProgress() {
			t.Error("unsupported file made config stale")
		}
		addedPath := filepath.Join(includePath, "20-metrics.yml")
		writeConfigSourceTestFile(t, addedPath, "metrics:\n  listen_port: 9191\n")
		if !config.HasConfigChanged() {
			t.Error("added config source was not detected without marking")
		}
		if !config.CheckAndMarkReloadInProgress() {
			t.Error("added config source did not make config stale")
		}
		if err := os.Remove(addedPath); err != nil {
			t.Fatal(err)
		}
		if !config.HasConfigChanged() {
			t.Error("removed config source was not detected without marking")
		}
		if !config.CheckAndMarkReloadInProgress() {
			t.Error("removed config source did not make config stale")
		}
	})

	t.Run("config directory", func(t *testing.T) {
		directoryPath := t.TempDir()
		writeConfigSourceTestFile(t, filepath.Join(directoryPath, "10-primary.yaml"), configSourceTestPrimary)
		config, err := Load([]string{"-config", directoryPath})
		if err != nil {
			t.Fatal(err)
		}
		config.MgmtConfig.ReloadRateLimit = 0

		addedPath := filepath.Join(directoryPath, "20-frontend.yaml")
		writeConfigSourceTestFile(t, addedPath, "frontend:\n  listen_port: 9001\n")
		if !config.HasConfigChanged() {
			t.Error("added directory source was not detected without marking")
		}
		if !config.IsStale() {
			t.Error("added directory source did not make config stale")
		}
		config.Main.configRateLimitTime = time.Time{}
		if !config.CheckAndMarkReloadInProgress() {
			t.Error("IsStale unexpectedly marked the source snapshot")
		}
		if err := os.Remove(addedPath); err != nil {
			t.Fatal(err)
		}
		if !config.HasConfigChanged() {
			t.Error("removed directory source was not detected without marking")
		}
		if !config.CheckAndMarkReloadInProgress() {
			t.Error("removed directory source did not make config stale")
		}
	})
}

func TestConfigSourceRateLimitAndClone(t *testing.T) {
	configPath, includePath := makeConfigSourceTestDirectory(t)
	config, err := Load([]string{"-config", configPath})
	if err != nil {
		t.Fatal(err)
	}
	clone := config.Clone()
	if clone.ConfigFilePath() != configPath ||
		clone.Main.configSourceFingerprint != config.Main.configSourceFingerprint ||
		clone.Main.configSourcePlan != config.Main.configSourcePlan {
		t.Fatal("clone did not retain config source state")
	}
	if !slices.Equal(clone.ConfigFilePaths(), config.ConfigFilePaths()) {
		t.Fatal("clone did not retain config source paths")
	}
	clone.Main.configSourcePaths[0] = "changed"
	if config.Main.configSourcePaths[0] == "changed" {
		t.Fatal("clone shares config source paths with original")
	}

	clone.Main.configRateLimitTime = time.Now().Add(time.Minute)
	originalFingerprint := clone.Main.configSourceFingerprint
	writeConfigSourceTestFile(t, filepath.Join(includePath, "added.yaml"), "frontend:\n  listen_port: 9001\n")
	if clone.CheckAndMarkReloadInProgress() {
		t.Error("rate-limited config source check triggered a reload")
	}
	if clone.Main.configSourceFingerprint != originalFingerprint {
		t.Error("rate-limited config source check changed the recorded snapshot")
	}
	clone.Main.configRateLimitTime = time.Time{}
	clone.MgmtConfig.ReloadRateLimit = 0
	if !clone.CheckAndMarkReloadInProgress() {
		t.Error("clone did not detect a config source added after cloning")
	}
}
