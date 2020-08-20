/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"

	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	rule "github.com/tricksterproxy/trickster/pkg/proxy/origins/rule/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	rwo "github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter/options"
	to "github.com/tricksterproxy/trickster/pkg/proxy/tls/options"
)

const emptyFilePath = "../../testdata/test.empty.conf"

// EmptyTestConfig returns an empty config based on the testdata empty conf
func emptyTestConfig() (*Config, string) {
	const path = emptyFilePath
	c, _, _ := Load("testing", "testing", []string{"-config", path})
	s, _ := ioutil.ReadFile(path)
	return c, string(s)
}

func TestClone(t *testing.T) {
	c1 := NewConfig()

	oc := c1.Origins["default"]
	c1.NegativeCacheConfigs["default"]["404"] = 10

	const expected = "trickster"

	oc.CompressableTypeList = []string{"text/plain"}
	oc.CompressableTypes = map[string]bool{"text/plain": true}
	oc.NegativeCacheName = "default"
	oc.NegativeCache = map[int]time.Duration{404: time.Duration(10) * time.Second}
	oc.FastForwardPath = po.New()
	oc.TLS = &to.Options{CertificateAuthorityPaths: []string{"foo"}}
	oc.HealthCheckHeaders = map[string]string{headers.NameAuthorization: expected}

	c1.Rules = map[string]*rule.Options{
		"test": {},
	}

	c2 := c1.Clone()
	x := c2.Origins["default"].HealthCheckHeaders[headers.NameAuthorization]
	if x != expected {
		t.Errorf("clone mismatch")
	}
}

func TestOriginConfigClone(t *testing.T) {
	c := NewConfig()
	oc1 := c.Origins["default"]
	oc2 := oc1.Clone()
	if oc2.Paths == nil {
		t.Error("expected non-nil cloned config")
	}
}

func TestString(t *testing.T) {
	c1 := NewConfig()

	c1.Origins["default"].Paths["test"] = &po.Options{}

	c1.Caches["default"].Redis.Password = "plaintext-password"

	s := c1.String()
	if !strings.Contains(s, `password = "*****"`) {
		t.Errorf("missing password mask: %s", "*****")
	}
}

func TestHideAuthorizationCredentials(t *testing.T) {
	hdrs := map[string]string{headers.NameAuthorization: "Basic SomeHash"}
	hideAuthorizationCredentials(hdrs)
	if hdrs[headers.NameAuthorization] != "*****" {
		t.Errorf("expected '*****' got '%s'", hdrs[headers.NameAuthorization])
	}
}

func TestCloneOriginConfig(t *testing.T) {

	oc := oo.New()
	oc.Hosts = []string{"test"}

	oc2 := oc.Clone()

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

	if c.Main.PprofServer != d.DefaultPprofServerName {
		t.Errorf("expected %s got %s", d.DefaultPprofServerName, c.Main.PprofServer)
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
 [rules]
   [rules.example]
   input_source = 'path'
   input_type = 'string'
   operation = 'prefix'
   next_route = 'test'
	 [rules.example.cases]
		 [rules.example.cases.1]
		 matches = ['trickster']
		 next_route = 'test'
 `

const testRewriter = `
 [request_rewriters]
   [request_rewriters.example]
	 instructions = [
	   ['path', 'set', '/api/v1/query'],
	   ['param', 'delete', 'start'],
	   ['param', 'delete', 'end'],
	   ['param', 'delete', 'step']
	 ]
 `

const testPaths = `
	 [origins.test.paths]
	   [origins.test.paths.root]
	   path = '/'
	   match_type = 'prefix'
	   handler = 'proxycache'
	   req_rewriter_name = 'example'
 `

func TestProcessOriginConfigs(t *testing.T) {

	c, _ := emptyTestConfig()
	c.Origins["test"].ReqRewriterName = "invalid"
	toml := c.String() + testRewriter
	err := c.loadTOMLConfig(toml, &Flags{})
	if err == nil {
		t.Error("expected error for invalid rewriter name")
	}

	toml = strings.Replace(strings.Replace(
		toml,
		`req_rewriter_name = "invalid"`,
		`req_rewriter_name = "example"`,
		-1), "['path',", "['patha',", -1,
	)

	err = c.loadTOMLConfig(toml, &Flags{})
	if err == nil {
		t.Error("expected error for rewriter compilation")
	}

	toml = strings.Replace(toml, "['patha',", "['path',", -1)
	err = c.loadTOMLConfig(toml, &Flags{})
	if err != nil {
		t.Error(err)
	}

	toml = strings.Replace(
		c.String(),
		"[origins.test.paths]",
		testPaths,
		-1,
	)

	err = c.loadTOMLConfig(toml, &Flags{})
	if err != nil {
		t.Error(err)
	}

	toml = strings.Replace(
		toml,
		` req_rewriter_name = 'example'`,
		` req_rewriter_name = 'invalid'`,
		-1,
	)

	err = c.loadTOMLConfig(toml, &Flags{})
	if err == nil || !strings.Contains(err.Error(), "invalid rewriter name") {
		t.Error("expected toml parsing error", err)
	}

}

func TestLoadTOMLConfig(t *testing.T) {

	c := NewConfig()
	err := c.loadTOMLConfig("[[", nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected end of table name") {
		t.Error("expected toml parsing error")
	}

	c, tml := emptyTestConfig()
	err = c.loadTOMLConfig(tml, &Flags{})
	if err != nil {
		t.Error(err)
	}
}

func TestIsStale(t *testing.T) {

	testFile := fmt.Sprintf("/tmp/trickster_test_config.%d.conf", time.Now().UnixNano())
	_, tml := emptyTestConfig()

	err := ioutil.WriteFile(testFile, []byte(tml), 0666)
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(testFile)

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

	err = ioutil.WriteFile(testFile, []byte(tml), 0666)
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

func TestFrontendConfigEqual(t *testing.T) {

	f1 := &FrontendConfig{}
	f2 := &FrontendConfig{}

	b := f1.Equal(f2)
	if !b {
		t.Errorf("expected %t got %t", true, b)
	}

}
