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

package prometheus

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestRegisterHandlers(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	c.RegisterHandlers(nil)
	if _, ok := c.Handlers()[mnQueryRange]; !ok {
		t.Errorf("expected to find handler named: %s", mnQueryRange)
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, providers.Prometheus, "/health", "debug")
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	dpc := client.DefaultPathConfigs(rsc.BackendOptions)

	// Find the path config with path "/"
	if !slices.ContainsFunc([]*po.Options(dpc),
		func(pathConfig *po.Options) bool {
			return pathConfig.Path == "/"
		}) {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 21
	if len(dpc) != expectedLen {
		t.Errorf("expected ordered length to be: %d got %d", expectedLen, len(dpc))
	}
}

func TestMergeablePaths(t *testing.T) {
	if len(MergeablePaths()) != 6 {
		t.Errorf("expected %d got %d", 6, len(MergeablePaths()))
	}
}

func TestMergeablePathConfigs(t *testing.T) {
	for _, prefix := range []string{"", "/prefix", "/v1/prometheus/"} {
		t.Run(prefix, func(t *testing.T) {
			c, err := NewClientWithHooks("merge", nil, nil, nil, Hooks{PathPrefix: prefix})
			if err != nil {
				t.Fatal(err)
			}
			routes := c.MergeablePathConfigs()
			if len(routes) != 6 {
				t.Fatalf("merge route count = %d, want 6", len(routes))
			}
			for _, route := range routes {
				if !slices.Contains(c.MergeablePaths(), route.Path) || !strings.HasPrefix(route.Path, strings.TrimSuffix(prefix, "/")+APIPath) {
					t.Errorf("unexpected merge route: %s", route.Path)
				}
				if !slices.Contains(route.Methods, http.MethodGet) {
					t.Errorf("GET missing for %s", route.Path)
				}
			}
		})
	}
	c, err := NewClientWithHooks("restricted", nil, nil, nil, Hooks{AllowedPaths: []string{APIPath + mnQuery}})
	if err != nil {
		t.Fatal(err)
	}
	if routes := c.MergeablePathConfigs(); len(routes) != 1 || routes[0].Path != APIPath+mnQuery {
		t.Fatalf("restricted merge routes = %+v", routes)
	}
}
