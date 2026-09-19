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

package pgwire

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	autht "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
	tlso "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

const (
	configTestName      = "pg-direct"
	configTestOriginURL = "postgres://origin:origin%20pw@db.example/trick%20ster"
	configTestAuthName  = "pg-clients"
	configTestPort      = "5432"
)

type testEngine struct {
	relayOnly bool
	tlsMode   string
}

func (testEngine) Name() string        { return providers.Postgres }
func (testEngine) DefaultPort() string { return configTestPort }
func (testEngine) Dialect() string     { return providers.Postgres }
func (e testEngine) Analyzer() sqlanalyzer.DialectAnalyzer {
	if e.relayOnly {
		return nil
	}
	return testAnalyzer
}
func (e testEngine) Defaults() EngineDefaults               { return EngineDefaults{UpstreamTLSMode: e.tlsMode} }
func (testEngine) TimeAxis(oid uint32) (TimeAxisKind, bool) { return StandardTimeAxis(oid) }
func (testEngine) TimeSemantics() TimeSemantics             { return TimeSemantics{} }

var testAnalyzer = cockroach.NewAnalyzer(cockroach.Options{
	BucketMatchers: []cockroach.BucketMatcher{cockroach.DateBinMatcher}, RoundUnalignedTimeBounds: true,
})

func configTestOptions() *bo.Options {
	o := bo.New()
	o.Name, o.Provider, o.OriginURL = configTestName, providers.TimescaleDB, configTestOriginURL
	o.AuthenticatorName = configTestAuthName
	o.AuthOptions = &autho.Options{Users: configtypes.EnvStringMap{testClientUser: testClientPass}}
	return o
}

func TestConfigFromOptions(t *testing.T) {
	c, err := ConfigFromOptions(configTestOptions(), testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	want := Upstream{
		Address: "db.example:" + configTestPort, Host: "db.example",
		User: "origin", Password: "origin pw", Database: "trick ster",
	}
	if c.Upstream != want || !c.Terminated() || c.Provider != providers.Postgres ||
		c.BackendName != configTestName || c.RestartKey == "" || c.IdleTimeout != pgo.DefaultIdleTimeout {
		t.Fatalf("unexpected config %+v", c)
	}

	passthrough := configTestOptions()
	passthrough.AuthOptions.ObserveOnly = true
	passthrough.OriginURL = "postgresql://db.example:6432"
	c, err = ConfigFromOptions(passthrough, testEngine{})
	if err != nil || c.Terminated() || c.Upstream.Address != "db.example:6432" || c.Upstream.User != "" {
		t.Fatalf("unexpected passthrough config %+v, %v", c, err)
	}
}

func TestConfigFromOptionsRejections(t *testing.T) {
	for name, mutate := range map[string]func(*bo.Options){
		"unparsable URL":    func(o *bo.Options) { o.OriginURL = "://bad" },
		"wrong scheme":      func(o *bo.Options) { o.OriginURL = "mysql://origin@db.example/x" },
		"no host":           func(o *bo.Options) { o.OriginURL = "postgres:///x" },
		"port out of range": func(o *bo.Options) { o.OriginURL = "postgres://origin@db.example:70000/x" },
		"no origin user":    func(o *bo.Options) { o.OriginURL = "postgres://db.example/x" },
		"no users":          func(o *bo.Options) { o.AuthOptions.Users = nil },
		"empty password":    func(o *bo.Options) { o.AuthOptions.Users[testClientUser] = "" },
		"bad verifier":      func(o *bo.Options) { o.AuthOptions.Users[testClientUser] = cred.SCRAMSHA256Prefix + "junk" },
		"missing users file": func(o *bo.Options) {
			o.AuthOptions.UsersFile = filepath.Join(t.TempDir(), "missing.csv")
			o.AuthOptions.UsersFileFormat = autht.CSVNoHeader
		},
		"half a server pair": func(o *bo.Options) { o.TLS = &tlso.Options{FullChainCertPath: "cert.pem"} },
		"require_tls alone":  func(o *bo.Options) { o.RequireTLS = true },
		"half a client pair": func(o *bo.Options) { o.TLS = &tlso.Options{ClientCertPath: "cert.pem"} },
		"unreadable CA": func(o *bo.Options) {
			o.Postgres = &pgo.Options{UpstreamTLSMode: pgo.TLSModeVerifyFull}
			o.TLS = &tlso.Options{CertificateAuthorityPaths: []string{filepath.Join(t.TempDir(), "missing.pem")}}
		},
	} {
		o := configTestOptions()
		mutate(o)
		if _, err := ConfigFromOptions(o, testEngine{}); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if _, err := ConfigFromOptions(nil, testEngine{}); err == nil {
		t.Fatal("expected nil options to be rejected")
	}
	if _, err := ConfigFromOptions(configTestOptions(), nil); err == nil {
		t.Fatal("expected a missing engine to be rejected")
	}
}

func TestUsersFileIsMerged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.csv")
	if err := os.WriteFile(path, []byte("from_file,file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := configTestOptions()
	o.AuthOptions.UsersFile, o.AuthOptions.UsersFileFormat = path, autht.CSVNoHeader
	c, err := ConfigFromOptions(o, testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Users["from_file"] != "file-secret" || c.Users[testClientUser] != testClientPass {
		t.Fatalf("unexpected users %v", c.Users)
	}
}

func TestUpstreamTLSModes(t *testing.T) {
	keyPath, certPath, cleanup, err := tlstest.GetTestKeyAndCertFiles("ca")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	build := func(mode string, tlsOptions *tlso.Options) *tls.Config {
		t.Helper()
		o := configTestOptions()
		o.Postgres = &pgo.Options{UpstreamTLSMode: mode}
		o.TLS = tlsOptions
		c, err := ConfigFromOptions(o, testEngine{})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		return c.Upstream.TLS
	}
	if build(pgo.TLSModeDisable, &tlso.Options{InsecureSkipVerify: true}) != nil {
		t.Fatal("disable must produce no TLS config, whatever the tls block says")
	}
	require := build(pgo.TLSModeRequire, nil)
	if !require.InsecureSkipVerify || require.VerifyConnection != nil ||
		!slices.Equal(require.NextProtos, []string{alpnPostgreSQL}) || require.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected require config %+v", require)
	}
	full := build(pgo.TLSModeVerifyFull, &tlso.Options{CertificateAuthorityPaths: []string{certPath}})
	if full.InsecureSkipVerify || full.ServerName != "db.example" || full.RootCAs == nil {
		t.Fatalf("unexpected verify-full config %+v", full)
	}
	named := build(pgo.TLSModeVerifyFull, &tlso.Options{ServerName: "override.example"})
	if named.ServerName != "override.example" {
		t.Fatalf("expected the configured server name, got %q", named.ServerName)
	}
	withClient := build(pgo.TLSModeRequire, &tlso.Options{ClientCertPath: certPath, ClientKeyPath: keyPath})
	if len(withClient.Certificates) != 1 {
		t.Fatal("expected the client certificate to be loaded")
	}

	// verify-ca accepts a chain to the configured root under any host name
	ca := build(pgo.TLSModeVerifyCA, &tlso.Options{
		CertificateAuthorityPaths: []string{certPath}, ExcludeSystemRoots: true,
	})
	if !ca.InsecureSkipVerify || ca.VerifyConnection == nil {
		t.Fatalf("unexpected verify-ca config %+v", ca)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	trusted := parseTestCertificate(t, certPEM)
	if err = ca.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted}}); err != nil {
		t.Fatalf("expected the trusted chain to verify: %v", err)
	}
	if err = ca.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{trusted, trusted}}); err != nil {
		t.Fatalf("expected a chain with an intermediate to verify: %v", err)
	}
	_, otherPEM, err := tlstest.GetTestKeyAndCert(true)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := parseTestCertificate(t, otherPEM)
	if err = ca.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{untrusted}}); err == nil {
		t.Fatal("expected an untrusted certificate to be rejected")
	}
	if err = verifyChain(nil, x509.NewCertPool()); err == nil {
		t.Fatal("expected an empty chain to be rejected")
	}
}

func parseTestCertificate(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestRestartKeyTracksRuntimeIdentity(t *testing.T) {
	keyPath, certPath, cleanup, err := tlstest.GetTestKeyAndCertFiles("")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	key := func(mutate func(*bo.Options)) string {
		t.Helper()
		o := configTestOptions()
		if mutate != nil {
			mutate(o)
		}
		c, err := ConfigFromOptions(o, testEngine{})
		if err != nil {
			t.Fatal(err)
		}
		return c.RestartKey
	}
	base := key(nil)
	if base != key(nil) {
		t.Fatal("the restart key must be stable")
	}
	for name, mutate := range map[string]func(*bo.Options){
		"origin":      func(o *bo.Options) { o.OriginURL = "postgres://origin@other.example/x" },
		"credentials": func(o *bo.Options) { o.AuthOptions.Users[testClientUser] = "rotated" },
		"auth mode":   func(o *bo.Options) { o.AuthOptions.ObserveOnly = true },
		"tls mode":    func(o *bo.Options) { o.Postgres = &pgo.Options{UpstreamTLSMode: pgo.TLSModeRequire} },
		"tls files": func(o *bo.Options) {
			o.TLS = &tlso.Options{FullChainCertPath: certPath, PrivateKeyPath: keyPath, ClientCertPath: "absent.pem", ClientKeyPath: "absent.key"}
		},
	} {
		if key(mutate) == base {
			t.Fatalf("%s: expected a different restart key", name)
		}
	}
}

func TestEngines(t *testing.T) {
	engines := NewEngines(testEngine{})
	if engines.Get(providers.Postgres) == nil || engines.Get(providers.TimescaleDB) == nil {
		t.Fatal("expected the engine under its name and its alias")
	}
	if engines.Get(providers.MySQL) != nil {
		t.Fatal("expected no engine for an unrelated provider")
	}
	if names := engines.Names(); !slices.Equal(names, []string{providers.Postgres, providers.TimescaleDB}) {
		t.Fatalf("unexpected names %v", names)
	}
}

func adapterTestConfig() *config.Config {
	c := config.NewConfig()
	c.Listeners[configTestName] = listenerconfig.New(configTestName)
	c.Listeners[configTestName].Protocol = listenerconfig.ProtocolPostgres
	backend := configTestOptions()
	backend.ListenerNames = []string{configTestName}
	c.Backends = bo.Lookup{configTestName: backend}
	return c
}

func TestNativeListenerAdapterContract(t *testing.T) {
	adapter := NewNativeListenerAdapter(NewEngines(testEngine{}))
	if adapter.Protocol() != listenerconfig.ProtocolPostgres || adapter.SupportsHTTP() {
		t.Fatalf("unexpected identity %q", adapter.Protocol())
	}
	if !adapter.ServesProvider(providers.Postgres) || !adapter.ServesProvider(providers.TimescaleDB) ||
		adapter.ServesProvider(providers.MySQL) || len(adapter.Providers()) != 2 {
		t.Fatalf("unexpected providers %v", adapter.Providers())
	}
	if adapter.Configured(nil) || adapter.Configured(listenerconfig.New(configTestName)) {
		t.Fatal("Configured() accepted a listener without postgres options")
	}
	configured := listenerconfig.New(configTestName)
	configured.Postgres = pgo.NewListener()
	if !adapter.Configured(configured) {
		t.Fatal("Configured() rejected postgres listener options")
	}
	if err := adapter.ValidateListener(nil); err == nil {
		t.Fatal("ValidateListener(nil) succeeded")
	}
	defaults := listenerconfig.New(configTestName)
	if err := adapter.ValidateListener(defaults); err != nil || defaults.Postgres == nil {
		t.Fatalf("ValidateListener(defaults) = %v, options = %+v", err, defaults.Postgres)
	}
	configured.Postgres.IdleTimeout = 0
	if err := adapter.ValidateListener(configured); err == nil {
		t.Fatal("ValidateListener() accepted an invalid timeout")
	}

	valid := configTestOptions()
	valid.Postgres = pgo.New()
	if err := adapter.ValidateBackend(valid); err != nil {
		t.Fatalf("ValidateBackend(valid) = %v", err)
	}
	valid.Postgres.UpstreamTLSMode = "prefer"
	if err := adapter.ValidateBackend(valid); err == nil {
		t.Fatal("ValidateBackend() accepted an unknown TLS mode")
	}
	if err := adapter.ValidateBackend(nil); err == nil {
		t.Fatal("ValidateBackend(nil) succeeded")
	}
	if err := adapter.ValidateUserRouter(nil, configTestName, nil); err == nil {
		t.Fatal("ValidateUserRouter() must reject user routing for now")
	}
	if adapter.RouteResolver(native.BuildRequest{}) != nil {
		t.Fatal("expected no route resolver")
	}
}

func TestNativeListenerAdapterDescribeAndBuild(t *testing.T) {
	adapter := NewNativeListenerAdapter(NewEngines(testEngine{}))
	if _, err := adapter.Describe(nil, "missing"); err == nil {
		t.Fatal("Describe(nil) succeeded")
	}
	c := adapterTestConfig()
	if _, err := adapter.Describe(c, "unmapped"); err == nil {
		t.Fatal("Describe(unmapped) succeeded")
	}
	descriptor, err := adapter.Describe(c, configTestName)
	if err != nil || descriptor.RestartKey == "" {
		t.Fatalf("Describe() = %+v, %v", descriptor, err)
	}
	tuned := c.Clone()
	tuned.Listeners[configTestName].Postgres = pgo.NewListener()
	tuned.Listeners[configTestName].Postgres.AllowMD5 = true
	if changed, err := adapter.Describe(tuned, configTestName); err != nil || changed.RestartKey == descriptor.RestartKey {
		t.Fatalf("listener options must change the restart key: %v", err)
	}
	request := native.BuildRequest{Config: c, ListenerName: configTestName, Listener: c.Listeners[configTestName]}
	server, err := adapter.Build(request)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if err = server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	invalid := c.Clone()
	invalid.Backends[configTestName].OriginURL = "://bad"
	if _, err = adapter.Describe(invalid, configTestName); err == nil {
		t.Fatal("Describe(invalid) succeeded")
	}
	if _, err = adapter.Build(native.BuildRequest{Config: invalid, ListenerName: configTestName}); err == nil {
		t.Fatal("Build(invalid) succeeded")
	}
	if _, err = adapter.Build(native.BuildRequest{}); err == nil {
		t.Fatal("Build(empty) succeeded")
	}
	unlisted := adapterTestConfig()
	delete(unlisted.Listeners, configTestName)
	if _, err = adapter.Build(native.BuildRequest{Config: unlisted, ListenerName: configTestName}); err == nil {
		t.Fatal("Build() with no such listener succeeded")
	}
}

func TestCancelRegistry(t *testing.T) {
	registry := newCancelRegistry()
	realSecret := []byte{1, 2, 3, 4}
	pid, secret, err := registry.register(4242, realSecret)
	if err != nil || pid == 0 || len(secret) != legacySecretLen {
		t.Fatalf("register() = %d, %v, %v", pid, secret, err)
	}
	target := registry.lookup(pid, secret)
	if target == nil || target.realPID != 4242 || !slices.Equal(target.realSecret, realSecret) {
		t.Fatalf("unexpected target %+v", target)
	}
	if registry.lookup(pid, []byte{0, 0, 0, 0}) != nil || registry.lookup(pid+1, secret) != nil {
		t.Fatal("a wrong secret or process id must not match")
	}
	_, long, err := registry.register(1, make([]byte, fakeLongSecretLen))
	if err != nil || len(long) != fakeLongSecretLen {
		t.Fatalf("expected a %d-byte secret, got %d, %v", fakeLongSecretLen, len(long), err)
	}
	// a wrapped counter must skip zero and any id still in use
	registry.nextPID = ^uint32(0)
	registry.targets[1] = &cancelTarget{}
	wrapped, _, err := registry.register(7, realSecret)
	if err != nil || wrapped == 0 || wrapped == 1 {
		t.Fatalf("expected a fresh non-zero id, got %d, %v", wrapped, err)
	}
	registry.release(pid)
	if registry.lookup(pid, secret) != nil {
		t.Fatal("a released key must not match")
	}
}

func TestEngineSeam(t *testing.T) {
	for oid, want := range map[uint32]TimeAxisKind{
		OIDTimestampTZ: TimeAxisTimestampTZ, OIDTimestamp: TimeAxisTimestamp, OIDDate: TimeAxisDate,
		OIDInt2: TimeAxisEpochInteger, OIDInt4: TimeAxisEpochInteger, OIDInt8: TimeAxisEpochInteger,
		OIDFloat8: TimeAxisEpochFloat, OIDNumeric: TimeAxisEpochNumeric,
	} {
		if got, ok := StandardTimeAxis(oid); !ok || got != want {
			t.Fatalf("oid %d: got %v %t, want %v", oid, got, ok, want)
		}
	}
	const oidText = 25
	if _, ok := StandardTimeAxis(oidText); ok {
		t.Fatal("text cannot carry a time axis")
	}

	c, err := ConfigFromOptions(configTestOptions(), testEngine{})
	if err != nil || c.Analyzer == nil || c.Dialect != providers.Postgres || c.MaxQuerySizeBytes != pgo.DefaultMaxQuerySizeBytes {
		t.Fatalf("expected an inspecting config, got %+v, %v", c, err)
	}
	proxyOnly := configTestOptions()
	proxyOnly.ProxyOnly = true
	if c, err = ConfigFromOptions(proxyOnly, testEngine{}); err != nil || c.Analyzer != nil {
		t.Fatalf("proxy_only must switch statement inspection off: %v", err)
	}
	if c, err = ConfigFromOptions(configTestOptions(), testEngine{relayOnly: true}); err != nil || c.Analyzer != nil {
		t.Fatalf("an engine with no analyzer only relays: %v", err)
	}

	// the engine's TLS default applies only when the backend names no mode
	if c, err = ConfigFromOptions(configTestOptions(), testEngine{tlsMode: pgo.TLSModeRequire}); err != nil || c.Upstream.TLS == nil {
		t.Fatalf("expected the engine default to enable TLS: %v", err)
	}
	explicit := configTestOptions()
	explicit.Postgres = &pgo.Options{UpstreamTLSMode: pgo.TLSModeDisable}
	if c, err = ConfigFromOptions(explicit, testEngine{tlsMode: pgo.TLSModeRequire}); err != nil || c.Upstream.TLS != nil {
		t.Fatalf("an explicit mode must override the engine default: %v", err)
	}
}
