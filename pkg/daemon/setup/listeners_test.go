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

package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"
)

func TestListenerEnabledOn(t *testing.T) {
	tests := []struct {
		configuredListener string
		listenerName       string
		want               bool
	}{
		{mgmt.ListenerNameMgmt, mgmt.ListenerNameMgmt, true},
		{mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics, false},
		{mgmt.ListenerNameMetrics, mgmt.ListenerNameMgmt, false},
		{mgmt.ListenerNameMetrics, mgmt.ListenerNameMetrics, true},
		{mgmt.ListenerNameBoth, mgmt.ListenerNameMgmt, true},
		{mgmt.ListenerNameBoth, mgmt.ListenerNameMetrics, true},
		{mgmt.ListenerNameOff, mgmt.ListenerNameMgmt, false},
		{mgmt.ListenerNameOff, mgmt.ListenerNameMetrics, false},
	}

	for _, test := range tests {
		if got := listenerEnabledOn(test.configuredListener, test.listenerName); got != test.want {
			t.Errorf("listenerEnabledOn(%q, %q) = %t, want %t",
				test.configuredListener, test.listenerName, got, test.want)
		}
	}
}

func TestDesiredListeners(t *testing.T) {
	c := config.NewConfig()
	c.Listeners["custom"] = listenerconfig.New("custom")
	c.Listeners["custom"].ListenPort = 9000
	c.Listeners["custom"].Active = true
	routers := map[string]router.Router{
		listenerconfig.DefaultFrontendName: lm.NewRouter(),
		"custom":                           lm.NewRouter(),
	}

	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter())
	for _, key := range []string{
		listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false),
		listenerKey(mgmt.ListenerNameMetrics, listenerconfig.ProtocolHTTP, false),
		listenerKey(mgmt.ListenerNameMgmt, listenerconfig.ProtocolHTTP, false),
		listenerKey("custom", listenerconfig.ProtocolHTTP, false),
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing desired listener %q", key)
		}
	}
	if _, ok := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)]; ok {
		t.Errorf("TLS listener should not be desired until ServeTLS is enabled")
	}
}

func TestDesiredRoutedMySQLListener(t *testing.T) {
	c := config.NewConfig()
	deactivateBuiltinListeners(c)
	c.Listeners["mysql-users"] = listenerconfig.New("mysql-users")
	c.Listeners["mysql-users"].Protocol = listenerconfig.ProtocolMySQL
	c.Listeners["mysql-users"].ListenPort = 8486
	c.Listeners["mysql-users"].Active = true
	router := bo.New()
	router.Name = "mysql-users"
	router.Provider = providers.ALB
	router.ListenerNames = []string{"mysql-users"}
	router.ALBOptions = ao.New()
	router.ALBOptions.MechanismName = names.MechanismUR
	router.ALBOptions.UserRouter = &uropt.Options{
		TargetProvider: providers.MySQL,
		Users: uropt.UserMappingOptionsByUser{
			"alice": {ToBackend: "mysql-a"},
		},
	}
	router.AuthenticatorName = "mysql-listener-clients"
	router.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"alice": "alice-password"},
	}
	target := bo.New()
	target.Name = "mysql-a"
	target.Provider = providers.MySQL
	target.OriginURL = "mysql://origin:password@127.0.0.1/database"
	target.AuthenticatorName = "mysql-clients"
	target.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"alice": "alice-password"},
	}
	c.Backends = bo.Lookup{"mysql-users": router, "mysql-a": target}

	got := desiredListeners(c, nil, nil, nil)
	key := listenerKey("mysql-users", listenerconfig.ProtocolMySQL, false)
	desired, ok := got[key]
	if !ok {
		t.Fatalf("missing routed MySQL listener %q", key)
	}
	if desired.native == nil {
		t.Fatalf("desired listener = %+v, want a native protocol adapter", desired)
	}
	if desired.native.Protocol() != listenerconfig.ProtocolMySQL {
		t.Fatalf("native protocol = %q, want mysql", desired.native.Protocol())
	}
	if desired.origin == "" {
		t.Fatal("native listener restart identity is empty")
	}
}

func TestListenerNeedsRestart(t *testing.T) {
	o := listenerconfig.New("custom")
	o.ListenPort = 9000
	old := desiredListener{address: "127.0.0.1", port: 9000, options: o}
	current := old
	current.router = lm.NewRouter()
	if listenerNeedsRestart(old, current) {
		t.Errorf("router-only update should not restart a listener")
	}
	current.port = 9001
	if !listenerNeedsRestart(old, current) {
		t.Errorf("port change should restart a listener")
	}

	hotSwap := o.Clone()
	maxBodySize := int64(1024)
	hotSwap.MaxRequestBodySizeBytes = &maxBodySize
	hotSwap.TruncateRequestBodyTooLarge = !o.TruncateRequestBodyTooLarge
	current = old
	current.options = hotSwap
	if listenerNeedsRestart(old, current) {
		t.Error("HTTP request middleware change should not restart a listener")
	}

	current = old
	current.options = o.Clone()
	current.options.ConnectionsLimit++
	if !listenerNeedsRestart(old, current) {
		t.Error("connection limit change should restart a listener")
	}
}

func TestApplyListenerConfigsReloadReconciliation(t *testing.T) {
	firstPort := availablePort(t)
	secondPort := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })

	conf := config.NewConfig()
	for _, name := range []string{listenerconfig.DefaultFrontendName, mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics} {
		conf.Listeners[name].ListenPort = 0
		conf.Listeners[name].TLSListenPort = 0
		conf.Listeners[name].Active = false
	}
	conf.Listeners["custom"] = listenerconfig.New("custom")
	conf.Listeners["custom"].ListenAddress = "127.0.0.1"
	conf.Listeners["custom"].ListenPort = firstPort
	conf.Listeners["custom"].Active = true

	firstRouter := markerRouter("first")
	applyListenerConfigs(conf, nil, map[string]router.Router{"custom": firstRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	key := listenerKey("custom", listenerconfig.ProtocolHTTP, false)
	waitForListener(t, group, key)
	original := group.Get(key)
	assertResponseBody(t, firstPort, "first")

	// A router-only reload must retain the socket and atomically swap handlers.
	secondConf := conf.Clone()
	secondRouter := markerRouter("second")
	applyListenerConfigs(secondConf, conf, map[string]router.Router{"custom": secondRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if group.Get(key) != original {
		t.Errorf("unchanged listener socket was restarted")
	}
	assertResponseBody(t, firstPort, "second")

	// Request middleware is carried by the swapped router and must not drain
	// the HTTP socket when its listener-facing configuration changes.
	thirdConf := secondConf.Clone()
	maxBodySize := int64(2048)
	thirdConf.Listeners["custom"].MaxRequestBodySizeBytes = &maxBodySize
	thirdConf.Listeners["custom"].TruncateRequestBodyTooLarge = true
	applyListenerConfigs(thirdConf, secondConf, map[string]router.Router{"custom": secondRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if group.Get(key) != original {
		t.Error("HTTP request middleware change restarted the listener socket")
	}

	// A port change drains the old socket and starts the replacement.
	fourthConf := thirdConf.Clone()
	fourthConf.Listeners["custom"].ListenPort = secondPort
	applyListenerConfigs(fourthConf, thirdConf, map[string]router.Router{"custom": secondRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	waitForListener(t, group, key)
	if group.Get(key) == original {
		t.Errorf("changed listener port did not restart the socket")
	}
	assertResponseBody(t, secondPort, "second")
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", firstPort)); err == nil {
		response.Body.Close()
		t.Errorf("old listener port is still accepting requests")
	}
}

func TestApplyMySQLListenerRestartsWhenTLSFileContentsRotate(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	port := availablePort(t)
	conf.Listeners["mysql1"] = listenerconfig.New("mysql1")
	conf.Listeners["mysql1"].Protocol = listenerconfig.ProtocolMySQL
	conf.Listeners["mysql1"].ListenAddress = "127.0.0.1"
	conf.Listeners["mysql1"].ListenPort = port
	conf.Listeners["mysql1"].Active = true
	tlsDir := t.TempDir()
	keyPath := filepath.Join(tlsDir, "server-key.pem")
	certPath := filepath.Join(tlsDir, "server-cert.pem")
	if err := tlstest.WriteTestKeyAndCert(false, keyPath, certPath); err != nil {
		t.Fatal(err)
	}
	backend := bo.New()
	backend.Name = "mysql1"
	backend.Provider = providers.MySQL
	backend.ListenerNames = []string{"mysql1"}
	backend.OriginURL = "mysql://origin:password@127.0.0.1/database"
	backend.AuthenticatorName = "mysql-clients"
	backend.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "client-password"},
	}
	backend.TLS.ServeTLS = true
	backend.TLS.FullChainCertPath = certPath
	backend.TLS.PrivateKeyPath = keyPath
	conf.Backends = bo.Lookup{"mysql1": backend}

	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	key := listenerKey("mysql1", listenerconfig.ProtocolMySQL, false)
	waitForListener(t, group, key)
	original := group.Get(key)
	if err := tlstest.WriteTestKeyAndCert(false, keyPath, certPath); err != nil {
		t.Fatal(err)
	}
	next := conf.Clone()
	applyListenerConfigs(next, conf, nil, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	waitForListener(t, group, key)
	if group.Get(key) == original {
		t.Fatal("MySQL listener did not restart after TLS file content rotation")
	}
}

func markerRouter(marker string) router.Router {
	r := lm.NewRouter()
	r.RegisterRoute("/", nil, nil, matching.PathMatchTypePrefix,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(marker))
		}))
	return r
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

func waitForListener(t *testing.T, group *listener.Group, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l := group.Get(key); l != nil && l.State() == listener.StateReady {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener %q did not become ready", key)
}

func assertResponseBody(t *testing.T, port int, want string) {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Errorf("response body = %q, want %q", body, want)
	}
}

func TestApplyListenerConfigsNoListeners(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	// nil and empty configs are no-ops
	applyListenerConfigs(nil, nil, nil, nil, nil, nil, nil, nil, group)
	conf := config.NewConfig()
	conf.Listeners = nil
	applyListenerConfigs(conf, nil, nil, nil, nil, nil, nil, nil, group)
}

// TestApplyListenerConfigsManagementRoutes covers the config-handler and pprof
// route registration on both the mgmt and metrics routers.
func TestApplyListenerConfigsManagementRoutes(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })

	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	conf.MgmtConfig.ConfigHandlerListener = mgmt.ListenerNameBoth
	conf.MgmtConfig.PprofListener = mgmt.ListenerNameBoth

	metricsRouter := lm.NewRouter()
	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), metricsRouter,
		nil, nil, nil, group)

	for _, path := range []string{"/metrics", conf.MgmtConfig.ConfigHandlerPath} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		metricsRouter.ServeHTTP(w, r)
		if w.Code == http.StatusNotFound {
			t.Errorf("expected %s to be registered on the metrics router", path)
		}
	}
}

func TestApplyListenerConfigsTLS(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key.pem")
	certPath := filepath.Join(dir, "test.cert.pem")
	if err := tlstest.WriteTestKeyAndCert(false, keyPath, certPath); err != nil {
		t.Fatal(err)
	}

	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })

	port := availablePort(t)
	conf := tlsTestConfig(t, keyPath, certPath, port)
	key := listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)

	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: markerRouter("tls")}
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(),
		nil, nil, nil, group)
	waitForListener(t, group, key)
	l := group.Get(key)
	if l == nil {
		t.Fatal("expected a TLS listener")
	}

	// An unchanged TLS listener is not restarted; its certificates are
	// refreshed in place instead.
	second := conf.Clone()
	second.Listeners[listenerconfig.DefaultFrontendName].ServeTLS = true
	applyListenerConfigs(second, conf, routers, http.NotFoundHandler(), lm.NewRouter(),
		nil, nil, nil, group)
	if group.Get(key) != l {
		t.Error("an unchanged TLS listener should not be restarted")
	}
}

func TestApplyListenerConfigsTLSCertError(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "bad.key.pem")
	certPath := filepath.Join(dir, "bad.cert.pem")
	if err := os.WriteFile(keyPath, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, []byte("not a cert\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	conf := tlsTestConfig(t, keyPath, certPath, availablePort(t))
	key := listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: lm.NewRouter()}

	// an unloadable key pair is logged and the listener is skipped
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(),
		nil, nil, nil, group)
	if group.Get(key) != nil {
		t.Error("a listener with unloadable certificates should not start")
	}

	// the same failure on the update path is also non-fatal
	updateListenerCertificates(conf, desiredListener{
		key: key, listenerName: listenerconfig.DefaultFrontendName, tls: true,
	}, group)
}

// TestUpdateListenerCertificatesNoCerts covers the early return taken when a
// listener resolves to an empty TLS configuration.
func TestUpdateListenerCertificatesNoCerts(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := config.NewConfig()
	updateListenerCertificates(conf, desiredListener{
		key:          listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true),
		listenerName: listenerconfig.DefaultFrontendName,
		tls:          true,
	}, group)
}

// deactivateBuiltinListeners prevents the well-known ports from being bound.
func deactivateBuiltinListeners(conf *config.Config) {
	for _, name := range []string{
		listenerconfig.DefaultFrontendName,
		mgmt.ListenerNameMgmt,
		mgmt.ListenerNameMetrics,
	} {
		conf.Listeners[name].ListenPort = 0
		conf.Listeners[name].TLSListenPort = 0
		conf.Listeners[name].Active = false
	}
}

// tlsTestConfig returns a config whose default listener serves TLS on port
// using the supplied key pair.
func tlsTestConfig(t *testing.T, keyPath, certPath string, port int) *config.Config {
	t.Helper()
	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	conf.Backends["default"].ListenerName = listenerconfig.DefaultFrontendName
	conf.Backends["default"].TLS = &to.Options{
		ServeTLS:          true,
		FullChainCertPath: certPath,
		PrivateKeyPath:    keyPath,
	}
	o := conf.Listeners[listenerconfig.DefaultFrontendName]
	o.Active = true
	o.ServeTLS = true
	o.TLSListenAddress = "127.0.0.1"
	o.TLSListenPort = port
	return conf
}

func TestListenerBindingReloadPreservesExistingConnections(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	conf.MgmtConfig.ReloadDrainTimeout = timeconv.Duration(50 * time.Millisecond)
	for _, name := range []string{"keep-http", "keep-native", "add-http", "add-native"} {
		lo := listenerconfig.New(name)
		lo.ListenAddress = "127.0.0.1"
		lo.ListenPort = availablePort(t)
		if strings.HasSuffix(name, "native") {
			lo.Protocol = listenerconfig.ProtocolClickHouse
		}
		conf.Listeners[name] = lo
	}
	o := bo.New()
	o.Name = "click"
	o.Provider = providers.ClickHouse
	o.ListenerNames = []string{"keep-http", "keep-native"}
	conf.Backends = bo.Lookup{"click": o}
	apply := func(current, old *config.Config, marker string) {
		t.Helper()
		if err := validate.Listeners(current); err != nil {
			t.Fatal(err)
		}
		nativeRouter := lm.NewRouter()
		nativeRouter.RegisterRoute("/", nil, []string{http.MethodPost}, matching.PathMatchTypeExact, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"meta":[{"name":"value","type":"String"}],"data":[{"value":%q}],"rows":1}`, marker)
		}))
		client, err := backends.NewTimeseriesBackend("click", current.Backends["click"], nil, nativeRouter, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		routers := map[string]router.Router{"keep-http": markerRouter(marker), "add-http": markerRouter(marker)}
		applyListenerConfigs(current, old, routers, http.NotFoundHandler(), lm.NewRouter(), nil, backends.Backends{"click": client}, nil, group)
		for _, name := range current.Backends["click"].ListenerNames {
			waitForListener(t, group, listenerKey(name, current.Listeners[name].Protocol, false))
		}
	}
	apply(conf, nil, "first")
	httpKey := listenerKey("keep-http", listenerconfig.ProtocolHTTP, false)
	nativeKey := listenerKey("keep-native", listenerconfig.ProtocolClickHouse, false)
	originalHTTP, originalNative := group.Get(httpKey), group.Get(nativeKey)
	address := fmt.Sprintf("127.0.0.1:%d", conf.Listeners["keep-http"].ListenPort)
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := chdriver.OpenDB(&chdriver.Options{Addr: []string{fmt.Sprintf("127.0.0.1:%d", conf.Listeners["keep-native"].ListenPort)}, ReadTimeout: time.Second})
	defer db.Close()
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	check := func(marker string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "http://"+address+"/", nil)
		if err := req.Write(conn); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != marker {
			t.Fatalf("HTTP connection: %s %v", body, err)
		}
		var got string
		if err := session.QueryRowContext(ctx, "SELECT 1").Scan(&got); err != nil || got != marker {
			t.Fatalf("native connection: %s %v", got, err)
		}
		if group.Get(httpKey) != originalHTTP || group.Get(nativeKey) != originalNative {
			t.Fatal("unchanged listener was replaced")
		}
	}
	check("first")
	for i, names := range [][]string{
		{"keep-http", "keep-native", "add-http", "add-native"},
		{"add-native", "keep-native", "keep-http", "add-http", "keep-native"},
		{"keep-native", "keep-http"},
	} {
		next := conf.Clone()
		next.Backends["click"].ListenerNames = names
		next.Backends["click"].ListenerName = "keep-http"
		marker := fmt.Sprintf("reload-%d", i)
		apply(next, conf, marker)
		check(marker)
		conf = next
	}
	if group.Get(listenerKey("add-native", listenerconfig.ProtocolClickHouse, false)) != nil || group.Get(listenerKey("add-http", listenerconfig.ProtocolHTTP, false)) != nil {
		t.Fatal("removed bindings still listen")
	}
	next := conf.Clone()
	next.Backends["click"].ListenerNames = []string{"keep-http"}
	apply(next, conf, "http-only")
	if group.Get(nativeKey) != nil || group.Get(httpKey) != originalHTTP {
		t.Fatal("removal affected the wrong listener")
	}
	if err := session.PingContext(ctx); err == nil {
		t.Fatal("removed native binding retained its session")
	}
}
