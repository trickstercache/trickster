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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	trerr "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

const (
	shutdownTestDelay = 400 * time.Millisecond
	shutdownTestDrain = 5 * time.Second
	shutdownTestLong  = 10 * time.Second
)

// guardSignals keeps a test-owned channel registered for termination signals so
// the process default disposition stays disabled while Start is not yet listening.
func guardSignals(t *testing.T) {
	t.Helper()
	guard := make(chan os.Signal, 16)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	t.Cleanup(func() {
		signal.Stop(guard)
		for {
			select {
			case <-guard:
			default:
				return
			}
		}
	})
}

// shutdownConfig serves on port, proxies to origin, and uses the given
// shutdown delay and drain window.
func shutdownConfig(port int, origin string, delay, drain time.Duration) string {
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
    origin_url: '%s'
mgmt:
  shutdown_delay: %s
  shutdown_drain_timeout: %s
`, port, origin, delay, drain)
}

func getStatus(port int, path string) (int, error) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// terminate sends SIGTERM until the readiness endpoint reports draining, so a
// signal raised before Start is listening is retried rather than lost.
func terminate(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		probe := time.Now().Add(time.Second)
		for time.Now().Before(probe) {
			if status, err := getStatus(port, mgmt.DefaultReadyHandlerPath); err == nil &&
				status == http.StatusServiceUnavailable {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("readiness never reported draining after SIGTERM")
}

func startForShutdown(t *testing.T, delay, drain time.Duration, origin http.Handler) (int, <-chan error) {
	t.Helper()
	guardSignals(t)
	server := httptest.NewServer(origin)
	t.Cleanup(server.Close)
	port := availablePort(t)
	path := writeConfig(t, t.TempDir(), shutdownConfig(port, server.URL, delay, drain))
	errs := make(chan error, 1)
	go func() { errs <- Start(context.Background(), "-config", path) }()
	waitForPort(t, port)
	if status, err := getStatus(port, mgmt.DefaultReadyHandlerPath); err != nil || status != http.StatusOK {
		t.Fatalf("ready = %d, %v; want 200", status, err)
	}
	return port, errs
}

func TestStartDrainsInFlightRequestsOnSIGTERM(t *testing.T) {
	originDelay := 2 * shutdownTestDelay
	port, errs := startForShutdown(t, shutdownTestDelay, shutdownTestDrain,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(originDelay)
			w.WriteHeader(http.StatusOK)
		}))
	result := make(chan error, 1)
	go func() {
		status, err := getStatus(port, "/test/")
		if err == nil && status != http.StatusOK {
			err = fmt.Errorf("status = %d; want 200", status)
		}
		result <- err
	}()
	time.Sleep(50 * time.Millisecond)

	terminate(t, port)
	// during the shutdown delay the listener still accepts new connections
	if status, err := getStatus(port, mgmt.DefaultPingHandlerPath); err != nil || status != http.StatusOK {
		t.Errorf("ping during shutdown delay = %d, %v; want 200", status, err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("in-flight request failed during drain: %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("in-flight request did not complete")
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("Start did not return after the drain")
	}
	if _, err := getStatus(port, mgmt.DefaultPingHandlerPath); err == nil {
		t.Error("listener still accepting after shutdown")
	}
}

func TestStartSecondSignalForcesClose(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	port, errs := startForShutdown(t, shutdownTestLong, shutdownTestLong,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		}))
	result := make(chan error, 1)
	go func() {
		_, err := getStatus(port, "/test/")
		result <- err
	}()
	time.Sleep(50 * time.Millisecond)

	terminate(t, port)
	started := time.Now()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("second signal did not cut the shutdown short")
	}
	if elapsed := time.Since(started); elapsed >= shutdownTestLong {
		t.Errorf("shutdown took %v; the second signal must skip the delay and drain", elapsed)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Error("in-flight request completed; want its connection force-closed")
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("in-flight connection was not closed")
	}
}

func TestShutdownWithoutListeners(t *testing.T) {
	si := &instance.ServerInstance{Readiness: &ready.State{}}
	shutdown(si, nil)
	if !si.Readiness.Draining() {
		t.Error("shutdown did not mark the instance as draining")
	}
	quiesced := make(chan struct{})
	close(quiesced)
	si = &instance.ServerInstance{Listeners: listener.NewGroup()}
	shutdown(si, quiesced)
}

func TestShutdownStopsHealthChecks(t *testing.T) {
	hc := healthcheck.New()
	stopped := make(chan bool, 1)
	hc.Subscribe(stopped)
	shutdown(&instance.ServerInstance{Readiness: &ready.State{}, HealthChecker: hc}, nil)
	select {
	case <-stopped:
	default:
		t.Error("shutdown left the health checker running")
	}
}

func TestStopWorkersYieldsToRunningReload(t *testing.T) {
	hc := healthcheck.New()
	stopped := make(chan bool, 1)
	hc.Subscribe(stopped)
	mtx.Lock()
	stopWorkers(&instance.ServerInstance{HealthChecker: hc})
	mtx.Unlock()
	select {
	case <-stopped:
		t.Error("a reload holding the lock owns the checker; shutdown must not stop it")
	default:
	}
	stopWorkers(&instance.ServerInstance{})
}

// blockReloads stubs the reload delegate so the first reload blocks until
// release is closed; entered closes when it starts.
func blockReloads(t *testing.T) (entered, release chan struct{}) {
	t.Helper()
	entered = make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	prev := reloadDelegate
	reloadDelegate = func(_ *instance.ServerInstance, _ string, _ ...string) (bool, error) {
		once.Do(func() { close(entered) })
		<-release
		return false, nil
	}
	t.Cleanup(func() { reloadDelegate = prev })
	return entered, release
}

// hangup sends SIGHUP until the stubbed reload reports it has started.
func hangup(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("reload never started")
}

func TestSecondSignalDuringBlockedReloadForcesClose(t *testing.T) {
	entered, release := blockReloads(t)
	t.Cleanup(func() { close(release) })
	port, errs := startForShutdown(t, shutdownTestLong, shutdownTestLong,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	hangup(t, entered)
	terminate(t, port)
	started := time.Now()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("second signal did not end the shutdown while a reload was blocked")
	}
	if elapsed := time.Since(started); elapsed >= shutdownTestLong {
		t.Errorf("shutdown took %v; the second signal must not wait for the reload", elapsed)
	}
	if _, err := getStatus(port, mgmt.DefaultPingHandlerPath); err == nil {
		t.Error("listener still accepting after the forced close")
	}
}

func TestReadinessDrainsDuringBlockedReload(t *testing.T) {
	entered, release := blockReloads(t)
	port, errs := startForShutdown(t, 0, shutdownTestDrain,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	hangup(t, entered)
	// termination must flip readiness while the reload is still blocked
	terminate(t, port)
	select {
	case err := <-errs:
		t.Fatalf("Start returned %v before the reload finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	if status, err := getStatus(port, mgmt.DefaultReadyHandlerPath); err != nil || status != http.StatusServiceUnavailable {
		t.Errorf("ready during blocked reload = %d, %v; want 503", status, err)
	}
	close(release)
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("Start did not return after the reload was released")
	}
}

func TestForcedShutdownFencesUnfinishedReload(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	var lateErr error
	var group *listener.Group
	latePort := availablePort(t)
	prev := reloadDelegate
	// the stubbed reload blocks, then tries to publish a listener the way a
	// reload past its config-read phase would
	reloadDelegate = func(si *instance.ServerInstance, _ string, _ ...string) (bool, error) {
		once.Do(func() {
			group = si.Listeners
			close(entered)
			<-release
			lateErr = si.Listeners.StartListener("late", "127.0.0.1", latePort, 0, nil,
				http.NotFoundHandler(), nil, nil, time.Second, nil)
			close(finished)
		})
		return false, nil
	}
	t.Cleanup(func() { reloadDelegate = prev })

	port, errs := startForShutdown(t, shutdownTestLong, shutdownTestLong,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	hangup(t, entered)
	terminate(t, port)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
	case <-time.After(shutdownTestDrain):
		t.Fatal("forced shutdown did not complete while the reload was blocked")
	}
	if !group.Closed() {
		t.Fatal("listener group must be closed once forced shutdown began")
	}
	// the reload now finishes; its listener publication must be refused
	close(release)
	select {
	case <-finished:
	case <-time.After(shutdownTestDrain):
		t.Fatal("reload goroutine did not finish after release")
	}
	if !errors.Is(lateErr, trerr.ErrListenerGroupClosed) {
		t.Errorf("late listener start = %v; want %v", lateErr, trerr.ErrListenerGroupClosed)
	}
	if _, err := getStatus(latePort, mgmt.DefaultPingHandlerPath); err == nil {
		t.Error("a listener published after shutdown is accepting traffic")
	}
	if group.Get("late") != nil {
		t.Error("refused listener was added to the group")
	}
}
