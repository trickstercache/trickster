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

package monitor

import (
	"bufio"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

const testWatchInterval = 10 * time.Millisecond

func writePair(t *testing.T, certPath, keyPath string, names ...string) {
	t.Helper()
	k, c, err := tlstest.GetTestKeyAndCertWithNames(names...)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, k, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, c, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

func testConfig(t *testing.T, interval time.Duration) (*config.Config, string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writePair(t, certPath, keyPath, "one.example.com")
	conf := config.NewConfig()
	lo := conf.Listeners[listenerconfig.DefaultFrontendName]
	lo.ServeTLS = true
	lo.TLSWatchInterval = timeconv.Duration(interval)
	conf.Backends = map[string]*bo.Options{
		"test": {
			ListenerNames: []string{listenerconfig.DefaultFrontendName},
			TLS: &to.Options{
				ServeTLS:          true,
				FullChainCertPath: certPath,
				PrivateKeyPath:    keyPath,
			},
		},
	}
	return conf, certPath, keyPath
}

func startTLSListener(t *testing.T, certPath, keyPath string) (*listener.Group, string, string) {
	t.Helper()
	tlsCfg, err := tlsServerConfig(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	lg := listener.NewGroup()
	key := listener.GroupKey(listenerconfig.DefaultFrontendName, "", true)
	go lg.StartListener(key, "127.0.0.1", 0, 0, tlsCfg,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}), nil, nil, time.Second, time.Second)
	if !waitFor(t, 5*time.Second, func() bool { return lg.Get(key) != nil }) {
		t.Fatal("listener not found in group")
	}
	l := lg.Get(key)
	if !l.WaitForReady(5 * time.Second) {
		t.Fatal("listener not ready")
	}
	t.Cleanup(func() { lg.Shutdown(0) })
	return lg, key, l.Addr().String()
}

func tlsServerConfig(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func handshakeSAN(t *testing.T, address string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", address, &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test cert
		ServerName:         "one.example.com",
	})
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	defer conn.Close()
	leaf := conn.ConnectionState().PeerCertificates[0]
	if len(leaf.DNSNames) == 0 {
		return ""
	}
	return leaf.DNSNames[0]
}

func TestMonitorRotationHotSwap(t *testing.T) {
	conf, certPath, keyPath := testConfig(t, testWatchInterval)
	lg, key, address := startTLSListener(t, certPath, keyPath)

	m := New()
	defer m.Close()
	m.Apply(conf, lg)

	// establish a keep-alive connection on the original cert
	established, err := tls.Dial("tcp", address, &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test cert
		ServerName:         "one.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer established.Close()
	reader := bufio.NewReader(established)
	doRequest := func() {
		t.Helper()
		if _, err := established.Write([]byte("GET / HTTP/1.1\r\nHost: one.example.com\r\n\r\n")); err != nil {
			t.Fatalf("write on established connection failed: %v", err)
		}
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("read on established connection failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status on established connection: %d", resp.StatusCode)
		}
	}
	doRequest()

	if san := handshakeSAN(t, address); san != "one.example.com" {
		t.Fatalf("expected original cert before rotation, got %s", san)
	}

	// rotate the pair in place; config remains untouched
	writePair(t, certPath, keyPath, "two.example.com")

	if !waitFor(t, 5*time.Second, func() bool {
		conn, err := tls.Dial("tcp", address, &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test cert
		})
		if err != nil {
			t.Fatalf("handshake failed during rotation: %v", err)
		}
		defer conn.Close()
		leaf := conn.ConnectionState().PeerCertificates[0]
		return len(leaf.DNSNames) > 0 && leaf.DNSNames[0] == "two.example.com"
	}) {
		t.Fatal("new handshakes did not pick up the rotated certificate")
	}

	// the established connection must be untouched by the swap
	doRequest()

	// a config reload arriving mid-rotation is safe and preserves watcher
	// continuity; a subsequent rotation is still detected
	m.Apply(conf, lg)
	doRequest()
	writePair(t, certPath, keyPath, "three.example.com")
	if !waitFor(t, 5*time.Second, func() bool {
		conn, err := tls.Dial("tcp", address, &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test cert
		})
		if err != nil {
			return false
		}
		defer conn.Close()
		leaf := conn.ConnectionState().PeerCertificates[0]
		return len(leaf.DNSNames) > 0 && leaf.DNSNames[0] == "three.example.com"
	}) {
		t.Fatal("rotation after reload not detected")
	}
	doRequest()

	if key == "" {
		t.Fatal("unexpected empty listener key")
	}
}

func storeFor(t *testing.T, lg *listener.Group, key string) tr.CertStore {
	t.Helper()
	l := lg.Get(key)
	if l == nil || l.CertSwapper() == nil {
		t.Fatal("no swapper for listener")
	}
	store, ok := l.CertSwapper().(tr.CertStore)
	if !ok {
		t.Fatal("swapper does not implement CertStore")
	}
	return store
}

func TestMonitorMemoryCerts(t *testing.T) {
	conf, certPath, keyPath := testConfig(t, testWatchInterval)
	lg, key, _ := startTLSListener(t, certPath, keyPath)

	m := New()
	defer m.Close()
	m.Apply(conf, lg)

	k, c, err := tlstest.GetTestKeyAndCertWithNames("secret.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMemoryCert("no-such-listener", "ns/name", c, k); err == nil {
		t.Error("expected error for unknown listener")
	}
	if err := m.SetMemoryCert(listenerconfig.DefaultFrontendName, "ns/name", c, k); err != nil {
		t.Fatal(err)
	}
	store := storeFor(t, lg, key)
	entries := store.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	idx := slices.IndexFunc(entries, func(e tr.EntryInfo) bool {
		return e.SourceKind == tr.SourceKindMemory
	})
	if idx < 0 || entries[idx].Key != "memory:ns/name" ||
		entries[idx].CommonName != "secret.example.com" {
		t.Errorf("unexpected memory entry: %+v", entries)
	}

	// an invalid pair must be rejected by the same coherence validation
	if err := m.SetMemoryCert(listenerconfig.DefaultFrontendName, "ns/name",
		c, []byte("garbage")); err == nil {
		t.Error("expected validation error for invalid memory pair")
	}

	if err := m.RemoveMemoryCert(listenerconfig.DefaultFrontendName, "ns/name"); err != nil {
		t.Fatal(err)
	}
	if entries := storeFor(t, lg, key).Entries(); len(entries) != 1 {
		t.Errorf("expected 1 entry after removal, got %d", len(entries))
	}
}

func TestMonitorDisabledWatch(t *testing.T) {
	conf, certPath, keyPath := testConfig(t, 0)
	lg, key, address := startTLSListener(t, certPath, keyPath)

	m := New()
	defer m.Close()
	m.Apply(conf, lg)

	// with watching disabled, the store is still resynced with keyed entries
	entries := storeFor(t, lg, key).Entries()
	if len(entries) != 1 || entries[0].SourceKind != tr.SourceKindFile {
		t.Fatalf("expected 1 file-sourced entry, got %+v", entries)
	}

	// but a rotation is not auto-detected
	writePair(t, certPath, keyPath, "two.example.com")
	time.Sleep(100 * time.Millisecond)
	if san := handshakeSAN(t, address); san != "one.example.com" {
		t.Errorf("rotation applied despite disabled watching; got %s", san)
	}
}

func TestMonitorUnreadableSourceKeepsStore(t *testing.T) {
	conf, certPath, keyPath := testConfig(t, 0)
	lg, key, _ := startTLSListener(t, certPath, keyPath)

	m := New()
	defer m.Close()

	for _, breakage := range []func(){
		func() { os.Remove(certPath) },
		func() { writePair(t, certPath, keyPath, "two.example.com"); os.Remove(keyPath) },
		func() {
			writePair(t, certPath, keyPath, "two.example.com")
			if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		breakage()
		m.cache = make(map[string]*tr.Entry) // force a reload attempt
		m.Apply(conf, lg)
		entries := storeFor(t, lg, key).Entries()
		if len(entries) != 1 || entries[0].SourceKind != tr.SourceKindConfig {
			t.Fatalf("store should retain its config-populated entry, got %+v", entries)
		}
	}
}

func TestMonitorApplyLifecycle(t *testing.T) {
	conf, certPath, keyPath := testConfig(t, testWatchInterval)
	lg, key, _ := startTLSListener(t, certPath, keyPath)

	before := runtime.NumGoroutine()
	m := New()
	m.Apply(conf, lg)

	// soak: rapid rotation loop; the store must track the latest coherent pair
	for i := range 5 {
		names := []string{"one.example.com", "two.example.com"}
		writePair(t, certPath, keyPath, names[i%2])
	}
	writePair(t, certPath, keyPath, "final.example.com")
	if !waitFor(t, 5*time.Second, func() bool {
		entries := storeFor(t, lg, key).Entries()
		return len(entries) == 1 && entries[0].CommonName == "final.example.com"
	}) {
		t.Fatal("store did not converge to the final rotated certificate")
	}

	// a reload that drops all TLS backends must stop the watchers
	confNoTLS := config.NewConfig()
	confNoTLS.Listeners[listenerconfig.DefaultFrontendName].ServeTLS = false
	m.Apply(confNoTLS, lg)
	m.Close()
	if !waitFor(t, 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= before
	}) {
		t.Errorf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
	}
}

func TestMonitorTracksEveryBackendListenerBinding(t *testing.T) {
	conf, _, _ := testConfig(t, 0)
	primary := conf.Listeners[listenerconfig.DefaultFrontendName]
	primary.Active = true
	other := primary.Clone()
	other.Active = true
	conf.Listeners["second"] = other
	conf.Backends["test"].ListenerNames = []string{listenerconfig.DefaultFrontendName, "second"}
	m := New()
	defer m.Close()
	group := listener.NewGroup()
	m.Apply(conf, group)
	if len(m.listeners) != 2 {
		t.Fatalf("tracked %d listeners, want 2", len(m.listeners))
	}
	for _, ln := range m.listeners {
		if len(ln.fileSets) != 1 {
			t.Fatal("binding is missing its certificate")
		}
	}
	next := conf.Clone()
	next.Backends["test"].ListenerNames = []string{"second"}
	m.Apply(next, group)
	if len(m.listeners) != 1 {
		t.Fatalf("removed binding still monitored: %v", m.listeners)
	}
	for _, ln := range m.listeners {
		if ln.name != "second" {
			t.Fatal("wrong binding retained")
		}
	}
}
