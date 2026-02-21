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
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
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

	s := c1.String()

	if !strings.Contains(s, `password: '*****'`) {
		t.Errorf("missing password mask: %s", "*****")
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
	c, _ := emptyTestConfig()
	err := c.Process()
	if err != nil {
		t.Error(err)
	}
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
