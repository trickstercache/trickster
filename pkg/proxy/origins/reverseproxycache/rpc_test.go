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

package reverseproxycache

import (
	"testing"

	"github.com/tricksterproxy/trickster/pkg/proxy/origins"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
)

func TestReverseProxyCacheClientInterfacing(t *testing.T) {

	// this test ensures the client will properly conform to the
	// Client interface

	c := &Client{name: "test"}
	var oc origins.Client = c

	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}

}

func TestNewNewClient(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c == nil {
		t.Errorf("expected client named %s", "test")
	}
}

func TestHTTPClient(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.HTTPClient() == nil {
		t.Errorf("expected HTTPClient for RPC client named %s", "test")
	}
}

func TestGetCache(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Cache() != nil {
		t.Errorf("expected nil Cache for RPC client named %s", "test")
	}
}

func TestClientName(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Name() != "test" {
		t.Errorf("expected RPC client named %s", "test")
	}
}

func TestSetCache(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	c.SetCache(nil)
	if c.Cache() != nil {
		t.Errorf("expected nil cache for client named %s", "test")
	}
}

func TestConfiguration(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Configuration() == nil {
		t.Error("expected non-nil config")
	}
}

func TestRouter(t *testing.T) {
	c, err := NewClient("test", oo.NewOptions(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Router() != nil {
		t.Error("expected nil router")
	}
}
