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

package reverseproxycache

import (
	"testing"

	"github.com/Comcast/trickster/internal/config"
)

func TestNewNewClient(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	if c == nil {
		t.Errorf("expected client named %s", "test")
	}
}

func TestHTTPClient(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	if c.HTTPClient() == nil {
		t.Errorf("expected HTTPClient for RPC client named %s", "test")
	}
}

func TestGetCache(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	if c.Cache() != nil {
		t.Errorf("expected nil Cache for RPC client named %s", "test")
	}
}

func TestClientName(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	if c.Name() != "test" {
		t.Errorf("expected RPC client named %s", "test")
	}
}

func TestSetCache(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	c.SetCache(nil)
	if c.Cache() != nil {
		t.Errorf("expected nil cache for client named %s", "test")
	}
}
