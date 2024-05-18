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
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwo "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

const emptyFilePath = "../../../testdata/test.empty.conf"

// EmptyTestConfig returns an empty config based on the testdata empty conf
func emptyTestConfig() (*Config, string) {
	const path = emptyFilePath
	c, _, err := Load("testing", "testing", []string{"-config", path})
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

	o.CompressibleTypeList = []string{"text/plain"}
	o.CompressibleTypes = map[string]interface{}{"text/plain": nil}
	o.NegativeCacheName = "default"
	o.NegativeCache = map[int]time.Duration{404: time.Duration(10) * time.Second}
	o.FastForwardPath = po.New()
	o.TLS = &to.Options{CertificateAuthorityPaths: []string{"foo"}}
	o.HealthCheck.Headers = map[string]string{headers.NameAuthorization: expected}

	c1.Rules = map[string]*rule.Options{
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

	c1.Backends["default"].Paths["test"] = &po.Options{}

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

func TestProcessPprofConfig(t *testing.T) {

	c := NewConfig()
	c.Main.PprofServer = ""

	err := c.processPprofConfig()
	if err != nil {
		t.Error(err)
	}

	if c.Main.PprofServer != DefaultPprofServerName {
		t.Errorf("expected %s got %s", DefaultPprofServerName, c.Main.PprofServer)
	}

	c.Main.PprofServer = "x"

	err = c.processPprofConfig()
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}

}

func TestSetDefaults(t *testing.T) {

	c, _ := emptyTestConfig()

	c.Main.PprofServer = "x"
	err := c.setDefaults(nil)
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}

	c.Main.PprofServer = "both"
	c.RequestRewriters = make(map[string]*rwo.Options)
	err = c.setDefaults(nil)
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}
}

const testRule = `
rules:
  example:
    input_source: path
    input_type: string
    operation: prefix
    next_route: test
    cases:
      '1':
        matches:
          - trickster
        next_route: test

`

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
      root:
        path: /
        match_type: prefix
        handler: proxycache
        req_rewriter_name: example
`

func TestProcessBackendOptions(t *testing.T) {

	c, _ := emptyTestConfig()
	c.Backends["test"].ReqRewriterName = "invalid"
	yml := c.String() + testRewriter
	err := c.loadYAMLConfig(yml, &Flags{})
	if err == nil {
		t.Error("expected error for invalid rewriter name")
	}

	yml = strings.Replace(strings.Replace(
		yml,
		`req_rewriter_name: invalid`,
		`req_rewriter_name: example`,
		-1), "- - path", "- - patha", -1,
	)

	err = c.loadYAMLConfig(yml, &Flags{})
	if err == nil {
		t.Error("expected error for rewriter compilation")
	}

	yml = strings.Replace(yml, "- - patha", "- - path", -1)
	err = c.loadYAMLConfig(yml, &Flags{})
	if err != nil {
		t.Error(err)
	}

	yml = strings.Replace(
		c.String(),
		"backends:\n  test:\n",
		testPaths,
		-1,
	)

	err = c.loadYAMLConfig(yml, &Flags{})
	if err != nil {
		t.Error(err)
	}

	yml = strings.Replace(
		yml,
		` req_rewriter_name: example`,
		` req_rewriter_name: invalid`,
		-1,
	)

	err = c.loadYAMLConfig(yml, &Flags{})
	if err == nil || !strings.Contains(err.Error(), "invalid rewriter name") {
		t.Error("expected yaml parsing error", err)
	}

}

func TestLoadYAMLConfig(t *testing.T) {

	c := NewConfig()
	err := c.loadYAMLConfig("[[", nil)

	if err == nil || !strings.Contains(err.Error(), "did not find expected node content") {
		t.Error("expected yaml parsing error")
	}

	c, tml := emptyTestConfig()
	err = c.loadYAMLConfig(tml, &Flags{})
	if err != nil {
		t.Error(err)
	}
}

func TestIsStale(t *testing.T) {

	testFile := t.TempDir() + "/trickster_test.conf"
	_, tml := emptyTestConfig()

	err := os.WriteFile(testFile, []byte(tml), 0666)
	if err != nil {
		t.Error(err)
	}

	c, _, _ := Load("testing", "testing", []string{"-config", testFile})
	c.ReloadConfig.RateLimitMS = 0

	if c.IsStale() {
		t.Error("expected non-stale config")
	}

	temp := c.Main.configFilePath
	c.Main.configFilePath = testFile + ".invalid"
	if c.IsStale() {
		t.Error("expected non-stale config")
	}
	c.Main.configFilePath = temp

	time.Sleep(time.Millisecond * 10)

	err = os.WriteFile(testFile, []byte(tml), 0666)
	if err != nil {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 10)

	c.ReloadConfig = nil
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
