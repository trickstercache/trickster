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

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

const (
	overlayTestPrefix      = reserved.NamePrefixKubeGateway
	overlayTestBackendName = overlayTestPrefix + "svc"
)

const overlayTestBackend = `
backends:
  ` + overlayTestBackendName + `:
    provider: rp
    origin_url: http://example.com
    path_routing_disabled: true
    hosts:
      - svc.example
`

type overlayStub struct {
	current atomic.Pointer[config.Overlay]
}

func (s *overlayStub) Overlay() *config.Overlay { return s.current.Load() }

func (s *overlayStub) set(data, version string) {
	s.current.Store(&config.Overlay{Data: []byte(data), Prefix: overlayTestPrefix, Version: version})
}

// reloadableConfig is runnableConfig without the reload rate limit so tests
// can reload back to back.
func reloadableConfig(port int) string {
	return runnableConfig(port) + "mgmt:\n  reload_rate_limit: 0s\n"
}

// writeConfigAtomically replaces dir/trickster.yaml via rename so a concurrent
// reload never observes a partially written file.
func writeConfigAtomically(dir, body string) error {
	tmp := filepath.Join(dir, "trickster.yaml.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "trickster.yaml"))
}

func startOverlayInstance(t *testing.T, path string) *instance.ServerInstance {
	t.Helper()
	conf, clients, err := setup.BootstrapConfig("-config", path)
	if err != nil {
		t.Fatal(err)
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	if err := setup.ApplyConfig(si, conf, clients, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if si.HealthChecker != nil {
			si.HealthChecker.Shutdown()
		}
	})
	return si
}

func TestReloadAppliesOverlayAndPreservesItAcrossFileReload(t *testing.T) {
	dir := t.TempDir()
	firstPort := availablePort(t)
	path := writeConfig(t, dir, reloadableConfig(firstPort))
	si := startOverlayInstance(t, path)
	waitForPort(t, firstPort)
	stub := &overlayStub{}
	si.OverlayProvider = stub

	if ok, err := Reload(si, "test", "-config", path); ok || err != nil {
		t.Fatalf("reload with no overlay and unchanged files = (%v, %v); want (false, nil)", ok, err)
	}

	stub.set(overlayTestBackend, "v1")
	if ok, err := Reload(si, "overlay", "-config", path); !ok || err != nil {
		t.Fatalf("overlay reload = (%v, %v); want (true, nil)", ok, err)
	}
	if si.Config.Backends[overlayTestBackendName] == nil || si.Backends[overlayTestBackendName] == nil {
		t.Fatal("overlay backend missing from the applied config or clients")
	}
	if si.Config.OverlayVersion() != "v1" {
		t.Errorf("overlay version = %q; want v1", si.Config.OverlayVersion())
	}
	if ok, err := Reload(si, "overlay", "-config", path); ok || err != nil {
		t.Fatalf("unchanged overlay reload = (%v, %v); want (false, nil)", ok, err)
	}

	// a file-driven reload must carry the current overlay forward
	secondPort := availablePort(t)
	writeConfig(t, dir, reloadableConfig(secondPort))
	if ok, err := Reload(si, reload.SourceSIGHUP, "-config", path); !ok || err != nil {
		t.Fatalf("file reload = (%v, %v); want (true, nil)", ok, err)
	}
	waitForPort(t, secondPort)
	if si.Config.Backends[overlayTestBackendName] == nil {
		t.Fatal("file reload dropped the overlay backend")
	}

	// an empty overlay with a new version removes the generated objects
	stub.set("", "v2")
	if ok, err := Reload(si, "overlay", "-config", path); !ok || err != nil {
		t.Fatalf("empty overlay reload = (%v, %v); want (true, nil)", ok, err)
	}
	if si.Config.Backends[overlayTestBackendName] != nil {
		t.Error("empty overlay left the generated backend in place")
	}
}

func TestReloadInvalidOverlayRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, reloadableConfig(0))
	si := startOverlayInstance(t, path)
	stub := &overlayStub{}
	si.OverlayProvider = stub
	oldConfig := si.Config

	stub.set("backends:\n  "+overlayTestBackendName+":\n    provider: rp\n", "missing-origin")
	if ok, err := Reload(si, "overlay", "-config", path); ok || err == nil {
		t.Fatalf("invalid overlay reload = (%v, %v); want (false, error)", ok, err)
	}
	if si.Config != oldConfig || si.Config.Backends[overlayTestBackendName] != nil {
		t.Fatal("invalid overlay replaced the running config")
	}

	stub.set("backends:\n  svc:\n    provider: rp\n    origin_url: http://example.com\n", "unprefixed")
	_, err := Reload(si, "overlay", "-config", path)
	if !errors.Is(err, config.ErrOverlayNamePrefix) {
		t.Fatalf("error = %v; want %v", err, config.ErrOverlayNamePrefix)
	}
	if si.Config != oldConfig {
		t.Fatal("unprefixed overlay replaced the running config")
	}

	stub.set(overlayTestBackend, "good")
	if ok, err := Reload(si, "overlay", "-config", path); !ok || err != nil {
		t.Fatalf("recovery reload = (%v, %v); want (true, nil)", ok, err)
	}
	if si.Config == oldConfig || si.Config.Backends[overlayTestBackendName] == nil {
		t.Fatal("valid overlay after failures was not applied")
	}
}

func TestReloadConcurrentOverlayAndFileReloads(t *testing.T) {
	dir := t.TempDir()
	port := availablePort(t)
	path := writeConfig(t, dir, reloadableConfig(port))
	si := startOverlayInstance(t, path)
	waitForPort(t, port)
	stub := &overlayStub{}
	si.OverlayProvider = stub
	stub.set(overlayTestBackend, "v0")

	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for round := range 5 {
				stub.set(overlayTestBackend, fmt.Sprintf("v%d-%d", worker, round))
				if _, err := Reload(si, "overlay", "-config", path); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		for round := range 5 {
			body := reloadableConfig(port) + fmt.Sprintf("# revision %d\n", round)
			if err := writeConfigAtomically(dir, body); err != nil {
				t.Error(err)
				return
			}
			if _, err := Reload(si, reload.SourceSIGHUP, "-config", path); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()

	stub.set(overlayTestBackend, "final")
	if ok, err := Reload(si, "overlay", "-config", path); !ok || err != nil {
		t.Fatalf("final reload = (%v, %v); want (true, nil)", ok, err)
	}
	if si.Config.OverlayVersion() != "final" || si.Config.Backends[overlayTestBackendName] == nil {
		t.Fatal("instance did not converge on the final overlay")
	}
	waitForPort(t, port)
}

func TestStartRegistersReloader(t *testing.T) {
	si := &instance.ServerInstance{}
	if currentOverlay(si) != nil || currentOverlay(nil) != nil {
		t.Fatal("instance without a provider must yield no overlay")
	}
	stub := &overlayStub{}
	stub.set(overlayTestBackend, "v1")
	si.OverlayProvider = stub
	if o := currentOverlay(si); o == nil || o.Version != "v1" {
		t.Fatal("provider overlay was not returned")
	}
}
