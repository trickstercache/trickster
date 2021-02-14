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

package options

import (
	"io/ioutil"
	"testing"
	"time"

	ho "github.com/tricksterproxy/trickster/pkg/backends/healthcheck/options"
	ro "github.com/tricksterproxy/trickster/pkg/backends/rule/options"
	co "github.com/tricksterproxy/trickster/pkg/cache/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestClone(t *testing.T) {
	p := po.New()
	o := New()
	o.Hosts = []string{"test"}
	o.CacheName = "test"
	o.CompressableTypes = map[string]bool{"test": true}
	o.Paths = map[string]*po.Options{"test": p}
	o.NegativeCache = map[int]time.Duration{1: 1}
	o.HealthCheck = &ho.Options{}
	o.FastForwardPath = p
	o.RuleOptions = &ro.Options{}
	o2 := o.Clone()
	if o2.CacheName != "test" {
		t.Error("clone failed")
	}

}

func TestValidateBackendName(t *testing.T) {

	err := ValidateBackendName("test")
	if err != nil {
		t.Error(err)
	}

	err = ValidateBackendName("frontend")
	if err == nil {
		t.Error("expected error for invalid backend name")
	}

}

func testConfig() (Lookup, string) {
	//_, toml := testutil.EmptyTestConfig()
	n := New()
	n.Name = "test"
	n.Provider = "test"
	n.OriginURL = "http://1"
	ol := Lookup{"test": n}

	b, err := ioutil.ReadFile("../../../testdata/test.empty.conf")
	if err != nil {
		panic(err)
	}
	// tml, err := toml.Decode(string(b), nil)
	// if err != nil {
	// 	panic(err)
	// }
	return ol, string(b)
}

func TestValidateConfigMappings(t *testing.T) {

	ol, _ := testConfig()
	o := ol["test"]
	ol["frontend"] = o
	err := ol.ValidateConfigMappings(ro.Lookup{}, co.Lookup{})
	if err == nil {
		t.Error("expected error for invalid backend name")
	}

}
