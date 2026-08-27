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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	logmgr "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

func init() {
	// keeps test output free of application log lines
	logger.SetLogger(logging.NoopLogger())
}

// writeConfig writes body to a temp trickster.yaml and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// quietListeners zeroes every listener port so ApplyConfig builds the full
// router tree without binding any well-known ports during tests. Listeners
// stay Active so backends still resolve to a router.
func quietListeners(conf *config.Config) {
	for _, o := range conf.Listeners {
		o.ListenPort = 0
		o.TLSListenPort = 0
		o.ServeTLS = false
	}
}

const minimalConfig = `
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
`

func TestLoadAndValidateSuccess(t *testing.T) {
	conf, err := LoadAndValidate("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conf.Backends["test"]; !ok {
		t.Errorf("expected backend 'test' in %v", conf.Backends)
	}
}

func TestLoadAndValidateNoBackends(t *testing.T) {
	// no -origin-url and no config file leaves zero usable backends
	if _, err := LoadAndValidate("-log-level", "info"); err == nil {
		t.Error("expected an error when no backends are configured")
	}
}

func TestLoadAndValidateBadFlags(t *testing.T) {
	if _, err := LoadAndValidate("-not-a-real-flag"); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

func TestLoadAndValidateMissingConfigFile(t *testing.T) {
	if _, err := LoadAndValidate("-config", filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing config file")
	}
}

// a load failure while -validate was requested also prints usage
func TestLoadAndValidateMissingConfigFileWithValidateFlag(t *testing.T) {
	_, err := LoadAndValidate("-validate-config", "-config", filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Error("expected an error for a missing config file")
	}
}

func TestLoadAndValidatePrintVersion(t *testing.T) {
	conf, err := LoadAndValidate("-version")
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil || conf.Flags == nil || !conf.Flags.PrintVersion {
		t.Fatal("expected a config with the PrintVersion flag set")
	}
}

func TestLoadAndValidateBadBackend(t *testing.T) {
	_, err := LoadAndValidate("-config", writeConfig(t, `
backends:
  test:
    provider: rp
`))
	if err == nil {
		t.Error("expected an error for a backend with no origin_url")
	}
}

func TestLoadAndValidateBadCache(t *testing.T) {
	_, err := LoadAndValidate("-config", writeConfig(t, `
caches:
  test:
    provider: bbolt
    index:
      max_size_objects: 10
      max_size_backoff_objects: 100
backends:
  test:
    provider: rpc
    cache_name: test
    origin_url: 'http://example.com'
`))
	if err == nil {
		t.Error("expected an error for an invalid cache index configuration")
	}
}

func TestValidateConfigRejectsGraphiteOriginAuthConflicts(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{"authorization with username", `
    graphite:
      origin_authorization: 'Bearer tok'
      origin_username: 'u'`, gro.ErrOriginAuthConflict},
		{"password without username", `
    graphite:
      origin_password: 'p'`, gro.ErrOriginAuthNoUser},
		{"credential with +Authorization path", `
    graphite:
      origin_username: 'u'
      origin_password: 'p'
    paths:
      - path: /render
        request_headers:
          '+authorization': 'appended'`, gro.ErrOriginAuthAppend},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, `
backends:
  test:
    provider: graphite
    origin_url: 'http://example.com'`+tc.body)
			// the -validate-config flow and ordinary startup must both reject
			// it with the specific origin-auth error, not just any failure
			_, err := LoadAndValidate("-validate-config", "-config", path)
			if !errors.Is(err, tc.err) {
				t.Errorf("-validate-config: expected %v, got %v", tc.err, err)
			}
			_, _, err = BootstrapConfig("-config", path)
			if !errors.Is(err, tc.err) {
				t.Errorf("startup: expected %v, got %v", tc.err, err)
			}
		})
	}
}

func TestLoadAndValidateInvalidConfig(t *testing.T) {
	_, err := LoadAndValidate("-config", writeConfig(t, `
logging:
  log_level: not-a-level
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
`))
	if err == nil {
		t.Error("expected an error for an invalid log level")
	}
}

func TestLoadAndValidateLoaderWarnings(t *testing.T) {
	// the legacy 'frontend' section produces a loader warning that is logged
	conf, err := LoadAndValidate("-config", writeConfig(t, `
frontend:
  listen_port: 57821
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(conf.LoaderWarnings) == 0 {
		t.Error("expected at least one loader warning")
	}
}

func TestInitLoggerIncludesAllConfigFiles(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "10-base.yaml")
	fragmentPath := filepath.Join(directory, "20-logging.yaml")
	if err := os.WriteFile(basePath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fragmentPath, []byte("logging:\n  log_level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadAndValidate("-config", directory)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "trickster.log")
	conf.Logging.LogFile = logPath
	conf.Logging.LogLevel = string(level.Info)
	activeLogger := initLogger(conf)
	activeLogger.Close()
	logger.SetLogger(logging.NoopLogger())

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "config=" + basePath + "," + fragmentPath
	if !strings.Contains(string(contents), want) {
		t.Errorf("log does not contain ordered config files %q: %s", want, contents)
	}
}

func TestBootstrapConfig(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil {
		t.Fatal("expected a config")
	}
	if clients == nil {
		t.Fatal("expected a backends lookup")
	}
}

func TestBootstrapConfigLoadError(t *testing.T) {
	if _, _, err := BootstrapConfig("-not-a-real-flag"); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

func TestBootstrapConfigPrintVersion(t *testing.T) {
	conf, clients, err := BootstrapConfig("-version")
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil || !conf.Flags.PrintVersion {
		t.Fatal("expected a config with PrintVersion set")
	}
	if clients != nil {
		t.Errorf("expected no clients for -version, got %v", clients)
	}
}

func TestBootstrapConfigValidateOnly(t *testing.T) {
	conf, clients, err := BootstrapConfig("-validate-config", "-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if conf == nil || !conf.Flags.ValidateConfig {
		t.Fatal("expected a config with ValidateConfig set")
	}
	if clients != nil {
		t.Errorf("expected no clients for -validate, got %v", clients)
	}
}

func TestBootstrapConfigRoutesRulesAndPoolsError(t *testing.T) {
	// two backends marked default fail during route registration, which only
	// happens after the config itself validates cleanly
	_, _, err := BootstrapConfig("-config", writeConfig(t, `
backends:
  test1:
    is_default: true
    provider: rp
    origin_url: 'http://example.com'
  test2:
    is_default: true
    provider: rp
    origin_url: 'http://example.com'
`))
	if err == nil {
		t.Error("expected an error for multiple default backends")
	}
}

func TestApplyConfigNilArgs(t *testing.T) {
	if err := ApplyConfig(nil, config.NewConfig(), nil, nil, nil, nil); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if err := ApplyConfig(&instance.ServerInstance{}, nil, nil, nil, nil, nil); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestApplyConfig(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf)
	conf.Main.ServerName = ""
	// the purge-by-key path is normalized to a trailing slash
	conf.MgmtConfig.PurgeByKeyHandlerPath = strings.TrimSuffix(conf.MgmtConfig.PurgeByKeyHandlerPath, "/")

	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}

	if err := ApplyConfig(si, conf, clients, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	if si.Config != conf {
		t.Error("expected the instance config to be the new config")
	}
	if si.HealthChecker == nil {
		t.Error("expected a health checker")
	}
	if conf.Main.ServerName == "" {
		t.Error("expected an inferred server name")
	}
	if !strings.HasSuffix(conf.MgmtConfig.PurgeByKeyHandlerPath, "/") {
		t.Errorf("purge-by-key path %q should end in a slash",
			conf.MgmtConfig.PurgeByKeyHandlerPath)
	}
	t.Cleanup(func() {
		if si.HealthChecker != nil {
			si.HealthChecker.Shutdown()
		}
	})

	// a second pass exercises the reload branches that tear down the prior
	// health checker and ALB pools
	conf2, clients2, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf2)
	if err := ApplyConfig(si, conf2, clients2, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	if si.Config != conf2 {
		t.Error("expected the instance config to be replaced on reload")
	}
}

func TestApplyConfigNilMgmtConfig(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf)
	conf.MgmtConfig = nil

	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	if err := ApplyConfig(si, conf, clients, nil, nil, group); err != nil {
		t.Fatal(err)
	}
	if conf.MgmtConfig == nil {
		t.Error("expected a default mgmt config to be created")
	}
	t.Cleanup(func() {
		if si.HealthChecker != nil {
			si.HealthChecker.Shutdown()
		}
	})
}

func TestApplyConfigAuthenticatorError(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf)
	conf.Authenticators = options.Lookup{
		"bad": &options.Options{Name: "bad", Provider: "not-a-provider"},
	}
	si := &instance.ServerInstance{}
	if err := ApplyConfig(si, conf, clients, nil, nil, listener.NewGroup()); err == nil {
		t.Error("expected an error for an unsupported authenticator")
	}
}

func TestApplyConfigTracingError(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf)
	old := conf.Clone()
	old.Logging.LogFile = filepath.Join(t.TempDir(), "reload.log")
	conf.Logging.LogFile = old.Logging.LogFile
	count := 5
	conf.Logging.Retention = &logmgr.RetentionOptions{Count: &count}
	oldOptions := old.Logging.ManagerOptions()
	h, err := logmgr.GetWriter(oldOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// a backend pointing at a tracing config that does not exist fails
	// tracer registration
	conf.Backends["test"].TracingConfigName = "nonexistent"

	var errorFuncCalls int
	si := &instance.ServerInstance{Config: old}
	err = ApplyConfig(si, conf, clients, nil, func() { errorFuncCalls++ }, listener.NewGroup())
	if err == nil {
		t.Fatal("expected a tracer registration error")
	}
	if errorFuncCalls != 1 {
		t.Errorf("errorFunc calls = %d, want 1", errorFuncCalls)
	}
	restored, err := logmgr.GetWriter(oldOptions)
	if err != nil {
		t.Fatalf("old log options were not restored: %v", err)
	}
	restored.Close()
}

func TestApplyConfigRouteRegistrationError(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	quietListeners(conf)
	logFile := filepath.Join(t.TempDir(), "startup.log")
	conf.Logging.LogFile = logFile
	// an unknown provider is rejected by route registration
	conf.Backends["test"].Provider = "not-a-provider"

	var errorFuncCalls int
	var loggedBeforeExit bool
	si := &instance.ServerInstance{}
	err = ApplyConfig(si, conf, clients, nil, func() {
		errorFuncCalls++
		b, _ := os.ReadFile(logFile)
		loggedBeforeExit = strings.Contains(string(b), "route registration failed")
	}, listener.NewGroup())
	if err == nil {
		t.Fatal("expected a route registration error")
	}
	if errorFuncCalls != 1 {
		t.Errorf("errorFunc calls = %d, want 1", errorFuncCalls)
	}
	if !loggedBeforeExit {
		t.Error("startup failure was not flushed before the exit callback")
	}
	logger.Logger().Close()
	logger.SetLogger(logging.NoopLogger())
	b, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(b), "route registration failed") {
		t.Errorf("startup failure missing from configured log: %q", string(b))
	}
}

func TestBuildAuthenticators(t *testing.T) {
	if err := buildAuthenticators(nil); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if err := buildAuthenticators(&config.Config{}); err != nil {
		t.Errorf("err = %v, want nil", err)
	}

	c := &config.Config{Authenticators: options.Lookup{
		"good": &options.Options{
			Name:     "good",
			Provider: basic.ID,
			Users:    map[string]string{"user1": "pass1"},
		},
	}}
	if err := buildAuthenticators(c); err != nil {
		t.Fatal(err)
	}
	if c.Authenticators["good"].Authenticator == nil {
		t.Error("expected a compiled authenticator")
	}

	c = &config.Config{Authenticators: options.Lookup{
		"bad": &options.Options{Name: "bad", Provider: "not-a-provider"},
	}}
	if err := buildAuthenticators(c); err == nil {
		t.Error("expected an error for an unsupported authenticator")
	}
}

func TestApplyLoggingConfigNoOps(t *testing.T) {
	before := logger.Logger()
	applyLoggingConfig(nil, nil)
	applyLoggingConfig(&config.Config{}, nil)
	if logger.Logger() != before {
		t.Error("logger should not be replaced when there is no logging config")
	}
}

func TestApplyLoggingConfigInitial(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		logger.Logger().Close()
		logger.SetLogger(logging.NoopLogger())
	})
	c := config.NewConfig()
	c.Logging.LogFile = filepath.Join(dir, "trickster.log")
	applyLoggingConfig(c, nil)
	if logger.Logger() == nil {
		t.Fatal("expected a logger")
	}
}

func TestApplyLoggingConfigUnchanged(t *testing.T) {
	t.Cleanup(func() { logger.SetLogger(logging.NoopLogger()) })
	old := config.NewConfig()
	nc := config.NewConfig()
	before := logger.Logger()
	applyLoggingConfig(nc, old)
	if logger.Logger() != before {
		t.Error("an unchanged logging config should retain the existing logger")
	}
}

func TestApplyLoggingConfigLevelChangeOnly(t *testing.T) {
	t.Cleanup(func() { logger.SetLogger(logging.NoopLogger()) })
	old := config.NewConfig()
	nc := config.NewConfig()
	nc.Logging.LogLevel = "debug"
	before := logger.Logger()
	applyLoggingConfig(nc, old)
	if logger.Logger() != before {
		t.Error("a log level change should retain the existing logger")
	}
}

func TestApplyLoggingConfigFileChange(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		logger.Logger().Close()
		logger.SetLogger(logging.NoopLogger())
	})
	old := config.NewConfig()
	old.Logging.LogFile = filepath.Join(dir, "old.log")
	old.MgmtConfig.ReloadDrainTimeout = 0
	logger.SetLogger(logging.New(old))

	nc := config.NewConfig()
	nc.Logging.LogFile = filepath.Join(dir, "new.log")
	nc.MgmtConfig = nil
	before := logger.Logger()
	applyLoggingConfig(nc, old)
	if logger.Logger() == before {
		t.Error("a log file change should install a new logger")
	}
	if nc.MgmtConfig == nil {
		t.Error("expected a default mgmt config to be created")
	}
	// let the delayed closer for the old logger run before TempDir cleanup
	time.Sleep(20 * time.Millisecond)
}

func TestApplyLoggingConfigRotationChange(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		logger.Logger().Close()
		logger.SetLogger(logging.NoopLogger())
	})
	old := config.NewConfig()
	old.Logging.LogFile = filepath.Join(dir, "trickster.log")
	old.MgmtConfig.ReloadDrainTimeout = 0
	logger.SetLogger(logging.New(old))

	// the shared writer is reconfigured before applying the logging config
	nc := config.NewConfig()
	nc.Logging.LogFile = old.Logging.LogFile
	nc.MgmtConfig.ReloadDrainTimeout = 0
	count := 5
	nc.Logging.Retention = &logmgr.RetentionOptions{Count: &count}
	before := logger.Logger()
	if err := reconfigureLogWriters(nc); err != nil {
		t.Fatal(err)
	}
	applyLoggingConfig(nc, old)
	if logger.Logger() != before {
		t.Error("a rotation-only change should retain the existing logger")
	}

	// an identical rotation config must retain the existing logger
	nc2 := config.NewConfig()
	nc2.Logging.LogFile = nc.Logging.LogFile
	nc2.Logging.Retention = &logmgr.RetentionOptions{Count: &count}
	before = logger.Logger()
	applyLoggingConfig(nc2, nc)
	if logger.Logger() != before {
		t.Error("an equivalent rotation config should retain the existing logger")
	}
}

func TestApplyCachingConfigNilArgs(t *testing.T) {
	if got := applyCachingConfig(nil, config.NewConfig()); got != nil {
		t.Errorf("got = %v, want nil", got)
	}
	if got := applyCachingConfig(&instance.ServerInstance{}, nil); got != nil {
		t.Errorf("got = %v, want nil", got)
	}
}

func TestApplyCachingConfigInitial(t *testing.T) {
	nc := config.NewConfig()
	caches := applyCachingConfig(&instance.ServerInstance{}, nc)
	if len(caches) != len(nc.Caches) {
		t.Fatalf("cache count = %d, want %d", len(caches), len(nc.Caches))
	}
	for _, c := range caches {
		_ = c.Close()
	}
}

func TestApplyCachingConfigReuseUnchanged(t *testing.T) {
	oc := config.NewConfig()
	existing := registry.NewCache("default", oc.Caches["default"])
	si := &instance.ServerInstance{
		Config: oc,
		Caches: cache.Lookup{"default": existing},
	}
	nc := config.NewConfig()
	caches := applyCachingConfig(si, nc)
	if caches["default"] != existing {
		t.Error("an unchanged cache should be reused")
	}
	_ = existing.Close()
}

func TestApplyCachingConfigMemoryIndexUpdate(t *testing.T) {
	oc := config.NewConfig()
	oc.Caches["default"].Provider = providers.Memory
	oc.Caches["default"].ProviderID = providers.MemoryID
	existing := registry.NewCache("default", oc.Caches["default"])
	if err := existing.Connect(); err != nil {
		t.Fatal(err)
	}
	si := &instance.ServerInstance{
		Config: oc,
		Caches: cache.Lookup{"default": existing},
	}

	nc := config.NewConfig()
	nc.Caches["default"].Provider = providers.Memory
	nc.Caches["default"].ProviderID = providers.MemoryID
	// an index-only delta keeps the same underlying cache
	nc.Caches["default"].Index.MaxSizeObjects = 12345

	caches := applyCachingConfig(si, nc)
	if caches["default"] != existing {
		t.Error("a memory cache with only index changes should be preserved")
	}
	_ = existing.Close()
}

func TestApplyCachingConfigClosesReplacedAndRemoved(t *testing.T) {
	oc := config.NewConfig()
	oc.Caches["default"].Provider = providers.Memory
	oc.Caches["default"].ProviderID = providers.MemoryID
	oc.Caches["gone"] = cacheoptions.New()
	oc.Caches["gone"].Name = "gone"

	replaced := registry.NewCache("default", oc.Caches["default"])
	if err := replaced.Connect(); err != nil {
		t.Fatal(err)
	}
	removed := registry.NewCache("gone", oc.Caches["gone"])
	if err := removed.Connect(); err != nil {
		t.Fatal(err)
	}
	si := &instance.ServerInstance{
		Config: oc,
		Caches: cache.Lookup{"default": replaced, "gone": removed},
	}

	nc := config.NewConfig()
	// a provider change means the old cache cannot be reused
	nc.Caches["default"].Provider = providers.BBolt
	nc.Caches["default"].ProviderID = providers.BBoltID
	nc.Caches["default"].BBolt.Filename = filepath.Join(t.TempDir(), "test.db")
	nc.MgmtConfig.ReloadDrainTimeout = 0

	caches := applyCachingConfig(si, nc)
	if caches["default"] == replaced {
		t.Error("a cache whose provider changed should not be reused")
	}
	if _, ok := caches["gone"]; ok {
		t.Error("a cache absent from the new config should not carry over")
	}
	// the old caches are closed on background goroutines
	time.Sleep(50 * time.Millisecond)
	for _, c := range caches {
		_ = c.Close()
	}
}

func TestDelayedLogCloser(t *testing.T) {
	delayedLogCloser(nil, 0) // must not panic
	// a real file, not os.Stdout: Close() closes the underlying writer
	f, err := os.Create(filepath.Join(t.TempDir(), "delayed.log"))
	if err != nil {
		t.Fatal(err)
	}
	delayedLogCloser(logging.StreamLogger(f, level.Info), time.Millisecond)
}

func TestHandleStartupIssue(t *testing.T) {
	var calls int
	errorFunc := func() { calls++ }

	handleStartupIssue("", nil, nil)
	if calls != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}

	handleStartupIssue("", nil, errorFunc)
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}

	handleStartupIssue("some event", logging.Pairs{"detail": "x"}, errorFunc)
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}

	handleStartupIssue("some event", logging.Pairs{"detail": "x"}, nil)
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestInitLogger(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		logger.Logger().Close()
		logger.SetLogger(logging.NoopLogger())
	})
	c := config.NewConfig()
	c.Logging.LogFile = filepath.Join(dir, "init.log")
	l := initLogger(c)
	if l == nil {
		t.Fatal("expected a logger")
	}
}
