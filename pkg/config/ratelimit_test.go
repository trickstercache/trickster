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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
)

func TestLoadRateLimiters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trickster.yaml")
	body := `
backends:
  api:
    provider: prometheus
    origin_url: http://prom:9090
    rate_limiter_name: per-client
    paths:
      - path: /healthz
        rate_limiter_name: none
      - path: /login
        rate_limiter_name: login
listeners:
  default:
    rate_limiter_name: per-client
rate_limiters:
  per-client:
    keys: [client_ip]
    limit: 600
    window: 1m
  login:
    keys: [client_ip, path]
    limit: 10
    window: 1m
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimiters["per-client"] == nil || c.RateLimiters["per-client"].Limit != 600 {
		t.Fatalf("rate_limiters = %#v", c.RateLimiters["per-client"])
	}
	if c.RateLimiters["per-client"].KeySources != nil {
		t.Fatal("load compiled keys; compilation belongs to validation")
	}
	if c.Backends["api"].RateLimiterName != "per-client" || c.Listeners["default"].RateLimiterName != "per-client" {
		t.Fatal("rate_limiter_name was not decoded")
	}
	paths := c.Backends["api"].Paths
	if paths[0].RateLimiterName != reserved.ReferenceNone || paths[1].RateLimiterName != "login" {
		t.Fatalf("path names = %#v", paths)
	}
	c.Listeners["default"].RateLimiter = c.RateLimiters["per-client"]
	c.Backends["api"].RateLimiter = c.RateLimiters["per-client"]
	c.Backends["api"].Paths[1].RateLimiter = c.RateLimiters["login"]
	cloned := c.Clone()
	if cloned.RateLimiters["per-client"] == c.RateLimiters["per-client"] {
		t.Fatal("clone shared the limiter definition")
	}
	if cloned.Listeners["default"].RateLimiter != cloned.RateLimiters["per-client"] ||
		cloned.Backends["api"].RateLimiter != cloned.RateLimiters["per-client"] ||
		cloned.Backends["api"].Paths[1].RateLimiter != cloned.RateLimiters["login"] ||
		cloned.Backends["api"].Paths[0].RateLimiter != nil {
		t.Fatal("clone kept a pre-clone limiter pointer")
	}
	if cloned.Listeners["default"].RateLimiter == c.RateLimiters["per-client"] {
		t.Fatal("listener still references the original limiter")
	}
}

func TestRateLimiterOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trickster.yaml")
	body := `
backends:
  primary:
    provider: prometheus
    origin_url: http://prom:9090
rate_limiters:
  local:
    limit: 5
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := testOverlay(`
rate_limiters:
  `+overlayTestPrefix+`edge:
    limit: 9
`, "v1")
	c, err := LoadWithOverlay([]string{"-config", path}, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateLimiters["local"] == nil || c.RateLimiters["local"].Limit != 5 {
		t.Fatalf("file limiter = %#v", c.RateLimiters["local"])
	}
	if c.RateLimiters[overlayTestPrefix+"edge"] == nil || c.RateLimiters[overlayTestPrefix+"edge"].Limit != 9 {
		t.Fatal("overlay limiter was not merged")
	}
}

func TestSanitizedCloneRateLimiters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trickster.yaml")
	body := `
backends:
  api:
    provider: prometheus
    origin_url: http://prom:9090
    rate_limiter_name: per-client
    paths:
      - path: /healthz
        rate_limiter_name: none
listeners:
  default:
    rate_limiter_name: per-client
rate_limiters:
  per-client:
    limit: 10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RateLimiterName = "per-client"
	sanitized := c.SanitizedClone()
	name := sanitized.Backends["prom-1"].RateLimiterName
	if !strings.HasPrefix(name, "rate-limit-") {
		t.Fatalf("sanitized backend reference %q", name)
	}
	if sanitized.Listeners["default"].RateLimiterName != name || sanitized.RateLimiters[name] == nil ||
		sanitized.Kubernetes.Defaults.RateLimiterName != name {
		t.Fatal("sanitized references diverged")
	}
	if sanitized.Backends["prom-1"].Paths[0].RateLimiterName != reserved.ReferenceNone {
		t.Fatal("none was renamed")
	}
	if c.Backends["api"].RateLimiterName != "per-client" {
		t.Fatal("sanitize mutated the original")
	}
}
