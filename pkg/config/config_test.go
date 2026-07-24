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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	tracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const emptyFilePath = "../../testdata/test.empty.conf"

// EmptyTestConfig returns an empty config based on the testdata empty conf
func emptyTestConfig() (*Config, string) {
	const path = emptyFilePath
	c, err := Load([]string{"-config", path})
	if err != nil {
		panic("could not load empty test config: " + err.Error())
	}
	s, _ := os.ReadFile(path)
	return c, string(s)
}

func TestClone(t *testing.T) {
	c1 := NewConfig()

	o := c1.Backends["default"]
	c1.NegativeCacheConfigs["default"]["404"] = 10

	const expected = "trickster"

	o.CompressibleTypeList = []string{headers.ValueTextPlain}
	o.CompressibleTypes = sets.New(o.CompressibleTypeList)
	o.NegativeCacheName = "default"
	o.NegativeCache = map[int]time.Duration{404: time.Duration(10) * time.Second}
	o.FastForwardPath = po.New()
	o.TLS = &to.Options{CertificateAuthorityPaths: []string{"foo"}}
	o.HealthCheck.Headers = map[string]string{headers.NameAuthorization: expected}

	c1.Rules = rule.Lookup{
		"test": {},
	}

	c2 := c1.Clone()
	x := c2.Backends["default"].HealthCheck.Headers[headers.NameAuthorization]
	if x != expected {
		t.Errorf("clone mismatch")
	}
}

func TestBackendOptionsClone(t *testing.T) {
	c := NewConfig()
	oc1 := c.Backends["default"]
	oc2 := oc1.Clone()
	if oc2.Paths == nil {
		t.Error("expected non-nil cloned config")
	}
}

func TestString(t *testing.T) {
	c1 := NewConfig()

	c1.Backends["default"].Paths = append(c1.Backends["default"].Paths, &po.Options{Path: "test"})

	c1.Caches["default"].Redis.Password = "plaintext-password"
	c1.Authenticators = auth.Lookup{
		"basic": {
			Users: ct.EnvStringMap{
				"alice": "alice-password",
				"bob":   "bob-password",
			},
		},
	}

	s := c1.String()

	if !strings.Contains(s, `password: '*****'`) {
		t.Errorf("missing password mask: %s", "*****")
	}
	for _, want := range []string{"user1: '*****'", "user2: '*****'"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing redacted authenticator user %q in config:\n%s", want, s)
		}
	}
	for _, sensitive := range []string{"alice", "alice-password", "bob", "bob-password"} {
		if strings.Contains(s, sensitive) {
			t.Errorf("config contains sensitive authenticator value %q:\n%s", sensitive, s)
		}
	}
	if c1.Authenticators["basic"].Users["alice"] != "alice-password" {
		t.Error("String mutated the original authenticator users")
	}
}

func TestCloneBackendOptions(t *testing.T) {
	o := bo.New()
	o.Hosts = []string{"test"}

	oc2 := o.Clone()

	if len(oc2.Hosts) != 1 {
		t.Errorf("expected %d got %d", 1, len(oc2.Hosts))
		return
	}

	if oc2.Hosts[0] != "test" {
		t.Errorf("expected %s got %s", "test", oc2.Hosts[0])
	}
}

func TestCheckFileLastModified(t *testing.T) {
	c := NewConfig()

	if !c.CheckFileLastModified().IsZero() {
		t.Error("expected zero time")
	}

	c.Main.configFilePath = "\t\n"
	if !c.CheckFileLastModified().IsZero() {
		t.Error("expected zero time")
	}
}

func TestConfigProcess(t *testing.T) {
	t.Run("compiles and assigns rewriters", func(t *testing.T) {
		c := NewConfig()
		c.RequestRewriters = rwopts.Lookup{
			"rewrite": {
				Instructions: rwopts.RewriteList{{"path", "set", "/rewritten"}},
			},
		}
		backend := c.Backends["default"]
		backend.ReqRewriterName = "rewrite"
		path := &po.Options{Path: "/", ReqRewriterName: "rewrite"}
		backend.Paths = []*po.Options{path}

		if err := c.Process(); err != nil {
			t.Fatalf("Process returned an error: %v", err)
		}
		if c.CompiledRewriters["rewrite"] == nil {
			t.Fatal("expected Process to compile the configured rewriter")
		}
		if backend.ReqRewriter == nil {
			t.Error("expected Process to assign the backend rewriter")
		}
		if path.ReqRewriter == nil {
			t.Error("expected Process to assign the path rewriter")
		}
	})

	t.Run("rejects invalid rewriter instructions", func(t *testing.T) {
		c := NewConfig()
		c.RequestRewriters = rwopts.Lookup{
			"invalid": {
				Instructions: rwopts.RewriteList{{"invalid", "instruction"}},
			},
		}

		if err := c.Process(); err == nil {
			t.Fatal("expected invalid rewriter instructions to return an error")
		}
	})

	t.Run("rejects missing backend rewriter", func(t *testing.T) {
		c := NewConfig()
		c.RequestRewriters = rwopts.Lookup{}
		c.Backends["default"].ReqRewriterName = "missing"

		err := c.Process()
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("expected missing backend rewriter error, got %v", err)
		}
	})

	t.Run("rejects missing path rewriter", func(t *testing.T) {
		c := NewConfig()
		c.RequestRewriters = rwopts.Lookup{}
		c.Backends["default"].Paths = []*po.Options{{
			Path:            "/query",
			ReqRewriterName: "missing",
		}}

		err := c.Process()
		if err == nil || !strings.Contains(err.Error(), "path /query") {
			t.Fatalf("expected missing path rewriter error, got %v", err)
		}
	})

	t.Run("processes tracing defaults", func(t *testing.T) {
		c := NewConfig()
		c.RequestRewriters = nil
		c.TracingOptions = tracing.Lookup{
			"test": {},
		}

		if err := c.Process(); err != nil {
			t.Fatalf("Process returned an error: %v", err)
		}
		if c.TracingOptions["test"].ServiceName != tracing.DefaultTracerServiceName {
			t.Errorf("expected default tracing service name, got %q",
				c.TracingOptions["test"].ServiceName)
		}
		if c.TracingOptions["test"].Provider != tracing.DefaultTracerProvider {
			t.Errorf("expected default tracing provider, got %q",
				c.TracingOptions["test"].Provider)
		}
	})
}

const testRewriter = `
request_rewriters:
  example:
    instructions:
      - - path
        - set
        - /api/v1/query
      - - param
        - delete
        - start
      - - param
        - delete
        - end
      - - param
        - delete
        - step
`

const testPaths = `
backends:
  test:
    paths:
      - path: /
        match_type: prefix
        handler: proxycache
        req_rewriter_name: example
`

func TestProcessBackendOptions(t *testing.T) {
	c, _ := emptyTestConfig()
	yml := c.String() + testRewriter
	yml = strings.Replace(strings.Replace(
		yml,
		`req_rewriter_name: invalid`,
		`req_rewriter_name: example`,
		-1), "- - path", "- - patha", -1,
	)
	yml = strings.ReplaceAll(yml, "- - patha", "- - path")
	err := c.loadYAMLConfig(yml)
	if err != nil {
		t.Error(err)
	}

	yml = strings.ReplaceAll(
		c.String(),
		"backends:\n  test:\n",
		testPaths,
	)

	err = c.loadYAMLConfig(yml)
	if err != nil {
		t.Error(err)
	}
}

func TestLoadYAMLConfig(t *testing.T) {
	c := NewConfig()
	err := c.loadYAMLConfig("[[")

	if err == nil || !strings.Contains(err.Error(), "did not find expected node content") {
		t.Error("expected yaml parsing error")
	}

	c, yml := emptyTestConfig()
	err = c.loadYAMLConfig(yml)
	if err != nil {
		t.Error(err)
	}
}

func TestIsStale(t *testing.T) {
	testFile := t.TempDir() + "/trickster_test.conf"
	_, tml := emptyTestConfig()

	err := os.WriteFile(testFile, []byte(tml), 0o666)
	if err != nil {
		t.Error(err)
	}

	c, _ := Load([]string{"-config", testFile})
	c.MgmtConfig.ReloadRateLimit = 0

	if c.IsStale() {
		t.Error("expected non-stale config")
	}

	c.Main.configFilePath = testFile + ".invalid"
	if c.IsStale() {
		t.Error("expected non-stale config")
	}
	c.Main.configFilePath = testFile
	time.Sleep(time.Millisecond * 10)

	err = os.WriteFile(testFile, []byte(tml), 0o666)
	if err != nil {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 10)

	c.MgmtConfig = nil
	if !c.IsStale() {
		t.Error("expected stale config")
	}

	time.Sleep(time.Millisecond * 10)

	if c.IsStale() {
		t.Error("expected non-stale config")
	}
}

func TestCheckAndMarkReloadInProgress(t *testing.T) {
	testFile := filepath.Join(t.TempDir(), "trickster.conf")
	if err := os.WriteFile(testFile, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	initialModTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(testFile, initialModTime, initialModTime); err != nil {
		t.Fatal(err)
	}

	c := NewConfig()
	c.Main.configFilePath = testFile
	c.Main.configLastModified = initialModTime.Add(-time.Second)
	c.MgmtConfig = nil

	if !c.CheckAndMarkReloadInProgress() {
		t.Fatal("expected modified config to be marked for reload")
	}
	if !c.Main.configLastModified.Equal(initialModTime) {
		t.Errorf("expected last modified time %v, got %v",
			initialModTime, c.Main.configLastModified)
	}
	if c.MgmtConfig == nil {
		t.Fatal("expected default management config to be initialized")
	}

	// Bypass the rate limit to prove the recorded timestamp prevents a
	// duplicate reload of the same file version.
	c.Main.configRateLimitTime = time.Time{}
	if c.CheckAndMarkReloadInProgress() {
		t.Error("expected the same config version not to trigger another reload")
	}

	newModTime := initialModTime.Add(time.Second)
	if err := os.Chtimes(testFile, newModTime, newModTime); err != nil {
		t.Fatal(err)
	}
	c.Main.configRateLimitTime = time.Now().Add(time.Minute)
	if c.CheckAndMarkReloadInProgress() {
		t.Error("expected rate-limited check not to trigger a reload")
	}
	if !c.Main.configLastModified.Equal(initialModTime) {
		t.Error("rate-limited check unexpectedly marked the newer file version")
	}
}

func TestConfigFilePath(t *testing.T) {
	c, _ := emptyTestConfig()

	if c.ConfigFilePath() != emptyFilePath {
		t.Errorf("expected %s got %s", emptyFilePath, c.ConfigFilePath())
	}

	c.Main = nil
	if c.ConfigFilePath() != "" {
		t.Errorf("expected %s got %s", "", c.ConfigFilePath())
	}
}

func TestSetStalenessInfo(t *testing.T) {
	fp := "trickster"
	t1 := time.Now()
	t2 := t1.Add(-1 * time.Minute)

	mc := &MainConfig{}
	mc.SetStalenessInfo(fp, t1, t2)

	if fp != mc.configFilePath || !t1.Equal(mc.configLastModified) ||
		!t2.Equal(mc.configRateLimitTime) {
		t.Error("mismatch")
	}
}

func TestConfig_defaulting(t *testing.T) {
	// test the overall defaulting logic for the entire trickster config, using
	// existing documentation examples as input
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("Config defaulting test failed, if adding new config fields please run tests with UPDATE_GOLDENS=true to update the golden files with the new default values.")
		}
	})

	entries, err := os.ReadDir("../../examples/conf")
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		// load file
		file := filepath.Join("../../examples/conf", entry.Name())
		c := NewConfig()
		b, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("unable to read input file: %v", err)
			continue
		}
		// decode & clean file
		require.NoError(t, c.loadYAMLConfig(string(b)))
		clean(c)

		// compare output to golden file
		generatedOutput := c.String()
		goldenFile := filepath.Join("testdata", filepath.Base(file))
		// trigger update of golden file
		if os.Getenv("UPDATE_GOLDENS") == "true" {
			require.NoError(t, os.WriteFile(goldenFile, []byte(generatedOutput), 0o666))
			continue
		}
		b, err = os.ReadFile(goldenFile)
		require.NoError(t, err)
		expected := string(b)
		require.Equal(t, expected, generatedOutput)
	}
}

// remove any values that are non-deterministic
func clean(c *Config) {
	c.Main.ServerName = "trickster-test"
}
