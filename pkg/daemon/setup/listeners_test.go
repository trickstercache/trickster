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
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	logmgr "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/routing"
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

	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
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

	got := desiredListeners(c, nil, nil, nil, nil, nil)
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

	current = old
	current.options = o.Clone()
	current.options.TrustedProxies = []string{"10.0.0.0/8"}
	if listenerNeedsRestart(old, current) {
		t.Error("trusted proxy change without the PROXY protocol should not restart a listener")
	}
	current.options.ProxyProtocol = true
	if !listenerNeedsRestart(old, current) {
		t.Error("enabling the PROXY protocol should restart a listener")
	}
	old.options = current.options.Clone()
	current.options = current.options.Clone()
	current.options.TrustedProxies = append(current.options.TrustedProxies, "192.0.2.1")
	if !listenerNeedsRestart(old, current) {
		t.Error("trusted proxy change with the PROXY protocol should restart a listener")
	}
}

func TestDesiredListenersResolveClientIP(t *testing.T) {
	c := config.NewConfig()
	c.Listeners[listenerconfig.DefaultFrontendName].Active = true
	c.Listeners[listenerconfig.DefaultFrontendName].ListenPort = 1
	c.Listeners[listenerconfig.DefaultFrontendName].TrustedProxies = []string{"192.0.2.0/24"}
	raw := lm.NewRouter()
	var seen string
	if err := raw.RegisterRoute("/", nil, nil, matching.PathMatchTypePrefix,
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = request.ClientIP(r)
		})); err != nil {
		t.Fatal(err)
	}
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, nil)
	proxy := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set(headers.NameXForwardedFor, "203.0.113.9")
	proxy.router.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "203.0.113.9" {
		t.Errorf("client ip = %q; want the forwarded address from a trusted proxy", seen)
	}
	req.RemoteAddr = "198.51.100.1:1234"
	proxy.router.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "198.51.100.1" {
		t.Errorf("client ip = %q; want the untrusted peer address", seen)
	}
	if trustedProxies(&listenerconfig.Options{TrustedProxies: []string{"bad"}}) != nil {
		t.Error("an unparsable list must trust no proxy")
	}
	if proxyProtocolOptions(c.Listeners[listenerconfig.DefaultFrontendName]) != nil {
		t.Error("PROXY protocol options must be nil when disabled")
	}
	c.Listeners[listenerconfig.DefaultFrontendName].ProxyProtocol = true
	if o := proxyProtocolOptions(c.Listeners[listenerconfig.DefaultFrontendName]); o == nil || len(o.Trusted) != 1 {
		t.Error("PROXY protocol options must carry the trusted proxies")
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

const runtimeCertSAN = "runtime.example.com"

func TestUpdateListenerCertificatesPreservesRuntimeEntries(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key.pem")
	certPath := filepath.Join(dir, "test.cert.pem")
	if err := tlstest.WriteTestKeyAndCert(false, keyPath, certPath); err != nil {
		t.Fatal(err)
	}
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf := tlsTestConfig(t, keyPath, certPath, availablePort(t))
	conf.Backends["default"].ListenerNames = []string{listenerconfig.DefaultFrontendName}
	key := listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, true)
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: lm.NewRouter()}
	applyListenerConfigs(conf, nil, routers, http.NotFoundHandler(), lm.NewRouter(),
		nil, nil, nil, group)
	waitForListener(t, group, key)
	store, ok := group.Get(key).CertSwapper().(tr.CertStore)
	if !ok {
		t.Fatal("listener has no certificate store")
	}
	k, c, err := tlstest.GetTestKeyAndCertWithNames(runtimeCertSAN)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tr.ValidatePair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	store.SetEntry(tr.NewEntry(tr.SourceKindMemory+":"+runtimeCertSAN, tr.SourceKindMemory, cert))

	updateListenerCertificates(conf, desiredListener{
		key: key, listenerName: listenerconfig.DefaultFrontendName, tls: true,
	}, group)
	kinds := make(map[string]int)
	for _, e := range store.Entries() {
		kinds[e.SourceKind]++
	}
	if kinds[tr.SourceKindMemory] != 1 || kinds[tr.SourceKindConfig] != 1 {
		t.Fatalf("entry kinds after update = %v; want config replaced and memory kept", kinds)
	}
}

func TestDesiredListenersWrapDefaultAccessLog(t *testing.T) {
	c := config.NewConfig()
	c.AccessLog = &alo.Options{Filename: logmgr.StreamStdout}
	for _, name := range []string{listenerconfig.DefaultFrontendName, mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics} {
		c.Listeners[name].Active = true
		c.Listeners[name].ListenPort = 1
	}
	routerLogger := routing.RouterAccessLogger(c)
	if routerLogger == nil {
		t.Fatal("expected a router-level access logger from the default access_log")
	}
	t.Cleanup(routerLogger.Close)
	raw := lm.NewRouter()
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
	got := desiredListeners(c, routers, lm.NewRouter(), raw, routerLogger, nil)
	proxy := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
	if _, isRouter := proxy.router.(router.Router); isRouter {
		t.Error("proxy listener router was not wrapped with the default access logger")
	}
	mgmtListener := got[listenerKey(mgmt.ListenerNameMgmt, listenerconfig.ProtocolHTTP, false)]
	if _, isRouter := mgmtListener.router.(router.Router); isRouter {
		t.Error("mgmt listener router was not wrapped with the default access logger")
	}
	metricsListener := got[listenerKey(mgmt.ListenerNameMetrics, listenerconfig.ProtocolHTTP, false)]
	if metricsListener.router != http.Handler(raw) {
		t.Error("metrics listener router must not be access-logged")
	}
	// without a default access log the routers are passed through untouched
	got = desiredListeners(c, routers, lm.NewRouter(), raw, nil, nil)
	if got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)].router != http.Handler(raw) {
		t.Error("router was wrapped without a default access logger")
	}
}

func TestDesiredListenersGuardReservedRoutes(t *testing.T) {
	c := config.NewConfig()
	c.Listeners[listenerconfig.DefaultFrontendName].Active = true
	c.Listeners[listenerconfig.DefaultFrontendName].ListenPort = 1
	readyPath := c.MgmtConfig.ReadyHandlerPath
	hijack := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend"))
	})
	raw := lm.NewRouter()
	// a backend claims the readiness path globally and for a specific host
	if err := raw.RegisterRoute(readyPath, nil, nil, matching.PathMatchTypeExact, hijack); err != nil {
		t.Fatal(err)
	}
	if err := raw.RegisterRoute(readyPath, []string{"api.example.com"}, nil,
		matching.PathMatchTypeExact, hijack); err != nil {
		t.Fatal(err)
	}
	reserved := []mgmtRoute{{path: readyPath, handler: ready.HandlerFunc(&ready.State{}, nil)}}
	routers := map[string]router.Router{listenerconfig.DefaultFrontendName: raw}
	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter(), nil, reserved)
	proxy := got[listenerKey(listenerconfig.DefaultFrontendName, listenerconfig.ProtocolHTTP, false)]
	for _, host := range []string{"", "api.example.com"} {
		req := httptest.NewRequest(http.MethodGet, readyPath, nil)
		if host != "" {
			req.Host = host
		}
		w := httptest.NewRecorder()
		proxy.router.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable || w.Body.String() != ready.BodyNotReady {
			t.Errorf("host %q: readiness = %d %q; want the reserved handler", host, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/other", nil)
	w := httptest.NewRecorder()
	proxy.router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("non-reserved path = %d; want the router's 404", w.Code)
	}
	if guardReservedRoutes(nil, raw) != http.Handler(raw) {
		t.Error("no reserved routes must pass the router through")
	}
}

// lineEcho answers each line with the prefix and the line
func lineEcho(t *testing.T, prefix string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if line != "" {
						_, _ = conn.Write([]byte(prefix + line))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func streamExchange(t *testing.T, port int, line string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(reply)
}

func streamConfigFor(t *testing.T, port int, protocol, origin string) (*config.Config, backends.Backends) {
	t.Helper()
	conf := config.NewConfig()
	deactivateBuiltinListeners(conf)
	lo := listenerconfig.New("relay")
	lo.ListenAddress = "127.0.0.1"
	lo.ListenPort = port
	lo.Protocol = protocol
	conf.Listeners["relay"] = lo
	o := bo.New()
	o.Provider = providers.ReverseProxyShort
	o.OriginURL = "tcp://" + origin
	o.ListenerNames = []string{"relay"}
	if err := o.Initialize("db"); err != nil {
		t.Fatal(err)
	}
	conf.Backends = bo.Lookup{"db": o}
	if err := validate.Listeners(conf); err != nil {
		t.Fatal(err)
	}
	client, err := backends.New("db", o, nil, lm.NewRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return conf, backends.Backends{"db": client}
}

func TestDesiredListenersStream(t *testing.T) {
	conf, _ := streamConfigFor(t, 9000, listenerconfig.ProtocolUDP, "127.0.0.1:53")
	desired := desiredListeners(conf, nil, nil, nil, nil, nil)
	key := listenerKey("relay", listenerconfig.ProtocolUDP, false)
	d, ok := desired[key]
	if !ok || !d.stream || d.port != 9000 || d.router != nil || d.native != nil {
		t.Fatalf("stream listener not described: %+v", d)
	}
	conf.Listeners["relay"].ListenPort = 0
	if _, ok := desiredListeners(conf, nil, nil, nil, nil, nil)[key]; ok {
		t.Error("a stream listener without a port was described")
	}
}

func TestApplyStreamListenerRelaysAndSwapsOnReload(t *testing.T) {
	first, second := lineEcho(t, "first:"), lineEcho(t, "second:")
	port := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolTCP, first)
	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
	key := listenerKey("relay", listenerconfig.ProtocolTCP, false)
	waitForListener(t, group, key)
	original := group.Get(key)
	if got := streamExchange(t, port, "hello"); got != "first:hello" {
		t.Fatalf("reply = %q", got)
	}
	// an origin change swaps the routing table without restarting the socket
	second2, clients2 := streamConfigFor(t, port, listenerconfig.ProtocolTCP, second)
	applyListenerConfigs(second2, conf, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients2, nil, group)
	if group.Get(key) != original {
		t.Error("an origin change restarted the stream listener")
	}
	if got := streamExchange(t, port, "again"); got != "second:again" {
		t.Errorf("reply after reload = %q", got)
	}
	// a backend with no dialable origin routes nothing, and the connection is refused
	third := second2.Clone()
	hostless := bo.New()
	hostless.Provider = providers.ReverseProxyShort
	hostless.ListenerNames = []string{"relay"}
	third.Backends = bo.Lookup{"db": hostless}
	clientless, err := backends.New("db", hostless, nil, lm.NewRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	applyListenerConfigs(third, second2, nil, http.NotFoundHandler(), lm.NewRouter(), nil,
		backends.Backends{"db": clientless}, nil, group)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("a connection with no upstream was not closed")
	}
	// removing the listener drains it
	fourth := third.Clone()
	delete(fourth.Backends, "db")
	fourth.Listeners["relay"].Active = false
	applyListenerConfigs(fourth, third, nil, http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if group.Get(key) != nil {
		t.Error("a removed stream listener is still in the group")
	}
}

func TestApplyStreamListenerUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("udp:"), buf[:n]...), addr)
		}
	}()
	port := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolUDP, pc.LocalAddr().String())
	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
	key := listenerKey("relay", listenerconfig.ProtocolUDP, false)
	waitForListener(t, group, key)
	raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := client.Read(buf)
	if err != nil || string(buf[:n]) != "udp:ping" {
		t.Fatalf("reply = %q %v", buf[:n], err)
	}
	// a reload with the same listener keeps the socket and updates the relay in place
	applyListenerConfigs(conf.Clone(), conf, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
	if _, err := client.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	n, err = client.Read(buf)
	if err != nil || string(buf[:n]) != "udp:pong" {
		t.Fatalf("reply after reload = %q %v", buf[:n], err)
	}
}
