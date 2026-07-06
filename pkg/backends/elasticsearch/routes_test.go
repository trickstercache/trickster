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

package elasticsearch

import (
	"net/http"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestRegisterHandlers(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.RegisterHandlers(nil)
	for _, name := range []string{handlerHealth, handlerQuery, handlerProxyCache, providers.Proxy} {
		if _, ok := c.Handlers()[name]; !ok {
			t.Fatalf("expected to find handler named %q", name)
		}
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, http.StatusOK, "{}",
		nil, providers.Elasticsearch, "/_search", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc.BackendClient = backendClient.(*Client)
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	if !slices.ContainsFunc([]*po.Options(backendClient.Configuration().Paths),
		func(pathConfig *po.Options) bool {
			return pathConfig.Path == "/" && pathConfig.HandlerName == handlerQuery
		}) {
		t.Fatal("expected to find query path config")
	}

	const expectedLen = 3
	if len(backendClient.Configuration().Paths) != expectedLen {
		t.Fatalf("paths = %d, want %d", len(backendClient.Configuration().Paths), expectedLen)
	}
}

func TestDefaultHealthCheckConfig(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, http.StatusOK, "{}",
		nil, providers.Elasticsearch, "/_search", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hc := backendClient.(*Client).DefaultHealthCheckConfig()
	if got := hc.Path; got != "/_cluster/health" {
		t.Fatalf("health path = %q, want /_cluster/health", got)
	}
}
