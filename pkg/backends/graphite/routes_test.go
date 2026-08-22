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

package graphite

import (
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestRegisterHandlers(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	c.RegisterHandlers(nil)
	for _, name := range []string{"health", handlerRender, handlerProxyCache, providers.Proxy} {
		if _, ok := c.Handlers()[name]; !ok {
			t.Errorf("expected to find handler named: %s", name)
		}
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, providers.Graphite, "/health", "debug")
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

	dpc := client.DefaultPathConfigs(rsc.BackendOptions)
	if len(dpc) != 9 {
		t.Fatalf("expected ordered length to be: %d got %d", 9, len(dpc))
	}
	if dpc[0].Path != renderPath || dpc[0].HandlerName != handlerRender ||
		slices.Contains(dpc[0].CacheKeyParams, "from") || !slices.Contains(dpc[0].CacheKeyParams, upTarget) {
		t.Errorf("unexpected render path config %+v", dpc[0])
	}
	if last := dpc[len(dpc)-1]; last.Path != "/" || last.HandlerName != providers.Proxy {
		t.Errorf("expected / -> %s last, got %s -> %s", providers.Proxy, last.Path, last.HandlerName)
	}
	for _, p := range dpc[1 : len(dpc)-1] {
		if p.HandlerName != handlerProxyCache || p.ResponseHeaders["Cache-Control"] == "" {
			t.Errorf("expected %s to be object-cached with a TTL: %+v", p.Path, p)
		}
	}
}
