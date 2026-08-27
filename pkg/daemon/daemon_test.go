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

package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

func init() {
	// keeps test output free of application log lines
	logger.SetLogger(logging.NoopLogger())
}

// writeConfig writes body into dir/trickster.yaml and returns the path. Using
// an explicit dir lets a test rewrite the same path to trigger a reload.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "trickster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func availablePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// runnableConfig returns a config body that serves on port and leaves the
// mgmt and metrics listeners unbound.
func runnableConfig(port int) string {
	return fmt.Sprintf(`
listeners:
  default:
    address: 127.0.0.1
    port: %d
  mgmt:
    port: 0
  metrics:
    port: 0
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
`, port)
}

// unapplyableConfig writes a malformed credentials file into dir and returns a
// config body referencing it. Configuration validation only checks that the
// users file is readable, so this config validates but fails when ApplyConfig
// compiles the authenticator.
func unapplyableConfig(t *testing.T, dir string) string {
	t.Helper()
	usersFile := filepath.Join(dir, "users.csv")
	// a short row makes encoding/csv reject the file
	if err := os.WriteFile(usersFile, []byte("user,hash\na,b,c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`
listeners:
  default:
    port: 0
  mgmt:
    port: 0
  metrics:
    port: 0
authenticators:
  test_auth:
    provider: basic
    users_file: '%s'
    users_file_format: csv
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
`, usersFile)
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing is listening on port %d", port)
}

func TestStartPrintVersion(t *testing.T) {
	if err := Start(context.Background(), "-version"); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestStartValidateConfig(t *testing.T) {
	path := writeConfig(t, t.TempDir(), runnableConfig(0))
	if err := Start(context.Background(), "-validate-config", "-config", path); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestStartBootstrapError(t *testing.T) {
	if err := Start(context.Background(), "-not-a-real-flag"); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

func TestStartApplyConfigError(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, unapplyableConfig(t, dir))
	if err := Start(context.Background(), "-config", path); err == nil {
		t.Error("expected an error when the config cannot be applied")
	}
}

func TestStartServesAndShutsDownOnContextCancel(t *testing.T) {
	port := availablePort(t)
	path := writeConfig(t, t.TempDir(), runnableConfig(port))

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- Start(ctx, "-config", path) }()

	waitForPort(t, port)
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/trickster/ping", port))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("ping status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestHupNoExistingConfig(t *testing.T) {
	si := &instance.ServerInstance{}
	ok, err := Hup(si, "test")
	if ok {
		t.Error("expected no reload when the instance has no config")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestHupNotStale(t *testing.T) {
	// a config with no backing file path is never considered stale
	si := &instance.ServerInstance{Config: config.NewConfig()}
	ok, err := Hup(si, "test")
	if ok {
		t.Error("expected no reload for a non-stale config")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestHupBootstrapFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, runnableConfig(0))
	conf, err := setup.LoadAndValidate("-config", path)
	if err != nil {
		t.Fatal(err)
	}
	si := &instance.ServerInstance{Config: conf}
	// the reload reads from a path that no longer parses
	if err := os.WriteFile(path, []byte("\tnot: [valid yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := Hup(si, "test", "-config", path)
	if ok {
		t.Error("expected the reload to fail")
	}
	if err == nil {
		t.Error("expected an error from the failed reload")
	}
}

func TestHupApplyConfigFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, runnableConfig(0))
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
	oldConfig := si.Config
	oldBackends := si.Backends
	oldCaches := si.Caches
	oldHealthChecker := si.HealthChecker
	// the replacement config validates but cannot be applied
	writeConfig(t, dir, unapplyableConfig(t, dir))
	ok, err := Hup(si, "test", "-config", path)
	if ok {
		t.Error("expected the reload to fail")
	}
	if err == nil {
		t.Error("expected an error from the failed reload")
	}
	if si.Config != oldConfig {
		t.Error("config was not rolled back")
	}
	if len(si.Backends) != len(oldBackends) {
		t.Error("backends were not rolled back")
	}
	if len(si.Caches) != len(oldCaches) {
		t.Error("caches were not rolled back")
	}
	if si.HealthChecker != oldHealthChecker {
		t.Error("health checker was not rolled back")
	}
}

func TestHupSuccess(t *testing.T) {
	dir := t.TempDir()
	firstPort := availablePort(t)
	path := writeConfig(t, dir, runnableConfig(firstPort))

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
	waitForPort(t, firstPort)
	secondPort := availablePort(t)
	writeConfig(t, dir, runnableConfig(secondPort))
	ok, err := Hup(si, "test", "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the reload to succeed")
	}
	if si.Config == conf {
		t.Error("expected the instance to hold the reloaded config")
	}
	waitForPort(t, secondPort)
}

func TestHupConfigDirectoryAfterAddingSource(t *testing.T) {
	dir := t.TempDir()
	firstPort := availablePort(t)
	if err := os.WriteFile(filepath.Join(dir, "10-base.yaml"),
		[]byte(runnableConfig(firstPort)), 0o600); err != nil {
		t.Fatal(err)
	}

	conf, clients, err := setup.BootstrapConfig("-config", dir)
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
	waitForPort(t, firstPort)

	secondPort := availablePort(t)
	override := fmt.Sprintf("listeners:\n  default:\n    port: %d\n", secondPort)
	if err := os.WriteFile(filepath.Join(dir, "20-listener.yaml"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := Hup(si, "test", "-config", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the added directory source to trigger a reload")
	}
	if si.Config == conf {
		t.Error("expected the instance to hold the reloaded directory config")
	}
	waitForPort(t, secondPort)
}

func TestReloadGoroutinePanicHandler(t *testing.T) {
	// the handler only logs; it must tolerate any panic value
	reloadGoroutinePanic("test-site", "test-source")("boom", []byte("stack"))
}
