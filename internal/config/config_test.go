/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"
)

func TestCopy(t *testing.T) {
	c1 := NewConfig()

	oc := c1.Origins["default"]
	c1.NegativeCacheConfigs["default"]["404"] = 10

	oc.CompressableTypeList = []string{"text/plain"}
	oc.CompressableTypes = map[string]bool{"text/plain": true}
	oc.NegativeCacheName = "default"
	oc.NegativeCache = map[int]time.Duration{404: time.Duration(10) * time.Second}
	oc.FastForwardPath = NewPathConfig()
	oc.TLS = &TLSConfig{CertificateAuthorityPaths: []string{"foo"}}
	oc.HealthCheckHeaders = map[string]string{headers.NameAuthorization: "Basic SomeHash"}

	c2 := c1.copy()
	if !reflect.DeepEqual(c1, c2) {
		t.Errorf("copy mistmatch")
	}
}

func TestString(t *testing.T) {
	c1 := NewConfig()

	c1.Origins["default"].Paths["test"] = &PathConfig{}

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

	oc := NewOriginConfig()
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
