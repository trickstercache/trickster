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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
)

const (
	overlayTestPrefix      = reserved.NamePrefixKubeGateway
	overlayTestBackendName = overlayTestPrefix + "svc"
	overlayTestCacheName   = overlayTestPrefix + "cache"
)

const overlayTestData = `
backends:
  ` + overlayTestBackendName + `:
    provider: rpc
    origin_url: http://svc.default.svc:8080
    path_routing_disabled: true
    cache_name: ` + overlayTestCacheName + `
    hosts:
      - svc.example
caches:
  ` + overlayTestCacheName + `:
    provider: memory
`

func testOverlay(data, version string) *Overlay {
	return &Overlay{Data: []byte(data), Prefix: overlayTestPrefix, Version: version}
}

func TestOverlayNilSafety(t *testing.T) {
	var o *Overlay
	if !o.IsEmpty() || o.VersionString() != "" {
		t.Error("nil overlay must be empty with no version")
	}
	o = &Overlay{Version: "v1"}
	if !o.IsEmpty() || o.VersionString() != "v1" {
		t.Error("overlay without data must be empty but keep its version")
	}
}

func TestLoadWithOverlayAddsPrefixedObjects(t *testing.T) {
	configPath, includePath := makeConfigSourceTestDirectory(t)
	writeConfigSourceTestFile(t, filepath.Join(includePath, "20-primary.yaml"), `
backends:
  primary:
    origin_url: http://primary-override:9090
`)
	overlay := testOverlay(overlayTestData, "v1")
	c, err := LoadWithOverlay([]string{"-config", configPath}, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Backends) != 2 {
		t.Fatalf("backends = %d; want 2", len(c.Backends))
	}
	primary := c.Backends["primary"]
	if primary.OriginURL != "http://primary-override:9090" || len(primary.Hosts) != 2 {
		t.Errorf("fragment merge was disturbed by the overlay: origin=%q hosts=%v",
			primary.OriginURL, primary.Hosts)
	}
	svc := c.Backends[overlayTestBackendName]
	if svc == nil || svc.OriginURL != "http://svc.default.svc:8080" ||
		!svc.PathRoutingDisabled || len(svc.Hosts) != 1 {
		t.Fatalf("overlay backend not loaded as configured: %+v", svc)
	}
	if _, ok := c.Caches[overlayTestCacheName]; !ok {
		t.Error("overlay cache was not loaded")
	}
	if c.OverlayVersion() != "v1" {
		t.Errorf("overlay version = %q; want v1", c.OverlayVersion())
	}
	if c.ConfigFilePath() != configPath || len(c.ConfigFilePaths()) != 2 {
		t.Errorf("file source state lost: path=%q paths=%v", c.ConfigFilePath(), c.ConfigFilePaths())
	}
	if clone := c.Clone(); clone.OverlayVersion() != "v1" {
		t.Errorf("clone overlay version = %q; want v1", clone.OverlayVersion())
	}
	if c.Backends[overlayTestBackendName].Name != overlayTestBackendName {
		t.Error("overlay backend name was not initialized")
	}
}

func TestLoadWithOverlayRejections(t *testing.T) {
	tests := []struct {
		name       string
		overlay    string
		prefix     string
		target     error
		errorMatch string
	}{
		{name: "top-level main", overlay: "main:\n  server_name: x\n", target: ErrOverlaySection},
		{name: "top-level frontend", overlay: "frontend:\n  listen_port: 1\n", target: ErrOverlaySection},
		{name: "top-level mgmt", overlay: "mgmt:\n  reload_rate_limit: 1s\n", target: ErrOverlaySection},
		{
			name:    "unprefixed backend",
			overlay: "backends:\n  svc:\n    provider: rp\n    origin_url: http://x\n",
			target:  ErrOverlayNamePrefix,
		},
		{name: "unprefixed listener", overlay: "listeners:\n  extra:\n    port: 0\n", target: ErrOverlayNamePrefix},
		{name: "section not a mapping", overlay: "backends:\n  - a\n", errorMatch: "must be a mapping"},
		{name: "malformed yaml", overlay: "[[", errorMatch: "parse config overlay"},
		{name: "multiple documents", overlay: "backends: {}\n---\ncaches: {}\n", errorMatch: "multiple YAML documents"},
		{
			name:       "duplicate key",
			overlay:    "backends:\n  " + overlayTestBackendName + ": {}\n  " + overlayTestBackendName + ": {}\n",
			errorMatch: "is repeated",
		},
		{name: "unreserved prefix", overlay: overlayTestData, prefix: "nope--", target: ErrOverlayPrefix},
		{name: "empty prefix", overlay: overlayTestData, target: ErrOverlayPrefix},
	}
	configPath, _ := makeConfigSourceTestDirectory(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			overlay := testOverlay(test.overlay, "v")
			if test.prefix != "" || test.target == ErrOverlayPrefix {
				overlay.Prefix = test.prefix
			}
			_, err := LoadWithOverlay([]string{"-config", configPath}, overlay)
			if err == nil {
				t.Fatal("expected an error")
			}
			if test.target != nil && !errors.Is(err, test.target) {
				t.Errorf("error = %v; want %v", err, test.target)
			}
			if test.errorMatch != "" && !strings.Contains(err.Error(), test.errorMatch) {
				t.Errorf("error = %v; want text %q", err, test.errorMatch)
			}
		})
	}
}

func TestLoadRejectsReservedNamesInFiles(t *testing.T) {
	const reservedBody = "backends:\n  " + overlayTestPrefix + "primary:\n    provider: prometheus\n    origin_url: http://p:9090\n"
	t.Run("single file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, reservedBody)
		if _, err := Load([]string{"-config", path}); !errors.Is(err, ErrReservedNamePrefix) {
			t.Fatalf("error = %v; want %v", err, ErrReservedNamePrefix)
		}
	})
	t.Run("fragment", func(t *testing.T) {
		path, includePath := makeConfigSourceTestDirectory(t)
		writeConfigSourceTestFile(t, filepath.Join(includePath, "20-reserved.yaml"), reservedBody)
		if _, err := Load([]string{"-config", path}); !errors.Is(err, ErrReservedNamePrefix) {
			t.Fatalf("error = %v; want %v", err, ErrReservedNamePrefix)
		}
	})
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigSourceTestFile(t, filepath.Join(dir, "10-primary.yaml"), configSourceTestPrimary)
		writeConfigSourceTestFile(t, filepath.Join(dir, "20-reserved.yaml"), reservedBody)
		if _, err := Load([]string{"-config", dir}); !errors.Is(err, ErrReservedNamePrefix) {
			t.Fatalf("error = %v; want %v", err, ErrReservedNamePrefix)
		}
	})
	t.Run("reserved cache name", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, configSourceTestPrimary+"caches:\n  "+overlayTestCacheName+":\n    provider: memory\n")
		if _, err := Load([]string{"-config", path}); !errors.Is(err, ErrReservedNamePrefix) {
			t.Fatalf("error = %v; want %v", err, ErrReservedNamePrefix)
		}
	})
}

func TestLoadOverlayOnly(t *testing.T) {
	if _, err := os.Stat(DefaultConfigPath); err == nil {
		t.Skip("default config path exists on this host")
	}
	c, err := LoadWithOverlay(nil, testOverlay(overlayTestData, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Backends) != 1 || c.Backends[overlayTestBackendName] == nil {
		t.Fatalf("backends = %v; want only the overlay backend", c.Backends)
	}
	if c.ConfigFilePath() != "" || c.ConfigFilePaths() != nil {
		t.Errorf("overlay-only config reported file sources: %q %v", c.ConfigFilePath(), c.ConfigFilePaths())
	}
	if c.OverlayVersion() != "v1" {
		t.Errorf("overlay version = %q; want v1", c.OverlayVersion())
	}
	c.MgmtConfig.ReloadRateLimit = 0
	if c.CheckAndMarkReloadInProgress("v1", true) {
		t.Error("unchanged overlay version reported stale")
	}
	if !c.CheckAndMarkReloadInProgress("v2", true) {
		t.Error("changed overlay version was not stale without file sources")
	}
	if c.CheckAndMarkReloadInProgress("v2", true) {
		t.Error("marked overlay version reported stale again")
	}
}

func TestLoadWithOverlayMissingCustomPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	overlay := testOverlay(overlayTestData, "v1")
	if _, err := LoadWithOverlay([]string{"-config", missing}, overlay); err == nil {
		t.Fatal("a missing explicit config path must fail even with an overlay")
	}
}

func TestCheckAndMarkReloadInProgressOverlay(t *testing.T) {
	configPath, includePath := makeConfigSourceTestDirectory(t)
	overlay := testOverlay(overlayTestData, "v1")
	c, err := LoadWithOverlay([]string{"-config", configPath}, overlay)
	if err != nil {
		t.Fatal(err)
	}
	c.MgmtConfig.ReloadRateLimit = 0
	if c.CheckAndMarkReloadInProgress("v1", true) || c.HasConfigChanged() {
		t.Fatal("freshly loaded config with overlay reported stale")
	}
	if !c.CheckAndMarkReloadInProgress("v2", true) {
		t.Error("overlay version change was not stale")
	}
	if c.OverlayVersion() != "v2" {
		t.Errorf("overlay version = %q; want v2 after marking", c.OverlayVersion())
	}
	if c.CheckAndMarkReloadInProgress("v2", true) {
		t.Error("marked overlay version reported stale again")
	}

	c.Main.configRateLimitTime = time.Now().Add(time.Minute)
	if c.CheckAndMarkReloadInProgress("v3", true) {
		t.Error("rate-limited check triggered a reload")
	}
	if c.OverlayVersion() != "v2" {
		t.Error("rate-limited check marked the overlay version")
	}
	if !c.CheckAndMarkReloadInProgress("v2b", false) || c.OverlayVersion() != "v2b" {
		t.Error("non-rate-limited check did not bypass the rate limit window")
	}
	if !time.Now().Before(c.Main.configRateLimitTime) {
		t.Error("non-rate-limited check altered the rate limit window")
	}
	c.Main.configRateLimitTime = time.Time{}

	writeConfigSourceTestFile(t, filepath.Join(includePath, "10-frontend.yaml"), "frontend:\n  listen_port: 9001\n")
	if !c.HasConfigChanged() {
		t.Error("file source change was not detected")
	}
	if !c.CheckAndMarkReloadInProgress("v3", true) {
		t.Error("combined file and overlay change was not stale")
	}
	if c.HasConfigChanged() || c.OverlayVersion() != "v3" {
		t.Error("combined check did not mark both the file sources and the overlay")
	}
	if c.CheckAndMarkReloadInProgress("v3", true) {
		t.Error("nothing changed but the config reported stale")
	}
}

func TestOverlaySectionsMatchConfigFields(t *testing.T) {
	tags := make(map[string]struct{})
	for field := range reflect.TypeFor[Config]().Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			tags[name] = struct{}{}
		}
	}
	for section := range overlaySections {
		if _, ok := tags[section]; !ok {
			t.Errorf("overlay section %q is not a Config yaml field", section)
		}
	}
	for _, fixed := range []string{"main", "frontend", "logging", "metrics", "mgmt"} {
		if _, ok := overlaySections[fixed]; ok {
			t.Errorf("section %q must not be overlay-writable", fixed)
		}
	}
}

// defaultPathFlags mimics a run without -config, where flags.ConfigPath is the
// default path and customPath is false.
func defaultPathFlags(path string) *Flags {
	return &Flags{ConfigPath: path}
}

func TestLoadFileDefaultPathErrorsPropagateWithOverlay(t *testing.T) {
	overlay := testOverlay(overlayTestData, "v1")
	t.Run("absent path is overlay-only", func(t *testing.T) {
		c := NewConfig()
		path := filepath.Join(t.TempDir(), "missing.yaml")
		if err := c.loadFile(defaultPathFlags(path), overlay); err != nil {
			t.Fatal(err)
		}
		if c.Backends[overlayTestBackendName] == nil || c.ConfigFilePath() != "" {
			t.Error("absent default path must load the overlay alone")
		}
		c = NewConfig()
		if err := c.loadFile(defaultPathFlags(path), nil); err != nil {
			t.Fatalf("absent default path without an overlay must not error: %v", err)
		}
	})
	t.Run("malformed primary", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, "[[")
		if err := NewConfig().loadFile(defaultPathFlags(path), overlay); err == nil {
			t.Fatal("a malformed default config must fail even with an overlay")
		}
	})
	t.Run("unreadable primary", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read mode-0 files")
		}
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, configSourceTestPrimary)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if err := NewConfig().loadFile(defaultPathFlags(path), overlay); err == nil {
			t.Fatal("an unreadable default config must fail even with an overlay")
		}
	})
	t.Run("missing required include", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, strings.Replace(configSourceTestPrimary,
			"main:\n", "main:\n  config_include_directory: gone\n", 1))
		if err := NewConfig().loadFile(defaultPathFlags(path), overlay); err == nil {
			t.Fatal("a missing required include must fail even with an overlay")
		}
	})
	t.Run("empty directory", func(t *testing.T) {
		if err := NewConfig().loadFile(defaultPathFlags(t.TempDir()), overlay); err == nil {
			t.Fatal("an empty default config directory must fail even with an overlay")
		}
	})
	t.Run("malformed overlay", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trickster.yaml")
		writeConfigSourceTestFile(t, path, configSourceTestPrimary)
		bad := testOverlay("[[", "v1")
		if err := NewConfig().loadFile(defaultPathFlags(path), bad); err == nil {
			t.Fatal("a malformed overlay must fail on the default path")
		}
		unprefixed := testOverlay("backends:\n  svc:\n    provider: rp\n    origin_url: http://x\n", "v1")
		if err := NewConfig().loadFile(defaultPathFlags(path), unprefixed); !errors.Is(err, ErrOverlayNamePrefix) {
			t.Fatalf("error = %v; want %v on the default path", err, ErrOverlayNamePrefix)
		}
	})
}
