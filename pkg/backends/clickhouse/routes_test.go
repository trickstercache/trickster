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

package clickhouse

import (
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
		t.Error(err)
	}
	c.RegisterHandlers(nil)
	if _, ok := c.Handlers()["query"]; !ok {
		t.Errorf("expected to find handler named: %s", "query")
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 204, "",
		nil, providers.ClickHouse, "/", "debug")
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

	// Find the path config with path "/"
	if !slices.ContainsFunc([]*po.Options(backendClient.Configuration().Paths),
		func(pathConfig *po.Options) bool {
			return pathConfig.Path == "/"
		}) {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 2
	if len(backendClient.Configuration().Paths) != expectedLen {
		t.Errorf("expected %d got %d", expectedLen, len(backendClient.Configuration().Paths))
	}
}

func TestDefaultPathConfigs_QueryInCacheKey(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := c.DefaultPathConfigs(nil)
	if len(paths) < 2 {
		t.Fatal("expected at least 2 paths")
	}
	// every parameter keys the query path, since query parameters and settings can change the
	// result, except transport-only ones such as the per-query id
	pc := paths[1]
	if len(pc.CacheKeyParams) != 1 || pc.CacheKeyParams[0] != "*" {
		t.Fatalf("CacheKeyParams must key every parameter: %v", pc.CacheKeyParams)
	}
	if !slices.Contains(pc.CacheKeyParamsExcluded, "query_id") {
		t.Errorf("query_id must be excluded from the key: %v", pc.CacheKeyParamsExcluded)
	}
	for _, keyed := range []string{"query", "database", "param_tenant", "max_result_rows", "session_id"} {
		if slices.Contains(pc.CacheKeyParamsExcluded, keyed) {
			t.Errorf("%s changes results and must stay in the key", keyed)
		}
	}
}
