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

package rule

import (
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/origins/rule/options"

	"testing"
)

func TestNewNewClient(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c == nil {
		t.Errorf("expected client named %s", "test")
	}
}

func TestHTTPClient(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.HTTPClient() != nil {
		t.Error("expected nil client")
	}
}

func TestGetCache(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Cache() != nil {
		t.Errorf("expected nil Cache for RPC client named %s", "test")
	}
}

func TestClientName(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Name() != "test" {
		t.Errorf("expected RPC client named %s", "test")
	}
}

func TestSetCache(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	c.SetCache(nil)
	if c.Cache() != nil {
		t.Errorf("expected nil cache for client named %s", "test")
	}
}

func TestConfiguration(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Configuration() == nil {
		t.Error("expected non-nil config")
	}
}

func TestRouter(t *testing.T) {
	c, err := NewClient("test", oo.New(), nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c.Router() != nil {
		t.Error("expected nil router")
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	c := &Client{}
	dpc := c.DefaultPathConfigs(nil)
	if dpc == nil {
		t.Error("expected non-nil path config")
	}
}

func TestRegisterHandlers(t *testing.T) {
	c := &Client{}
	c.registerHandlers()
	if _, ok := c.handlers["rule"]; !ok {
		t.Errorf("expected to find handler named: %s", "rule")
	}
}

func TestHandlers(t *testing.T) {
	c := &Client{}
	m := c.Handlers()
	if _, ok := m["rule"]; !ok {
		t.Errorf("expected to find handler named: %s", "rule")
	}
}

func TestValidate(t *testing.T) {
	c := Clients{&Client{options: &oo.Options{
		RuleOptions: &options.Options{InputType: "header"}}}}
	err := c.Validate(nil)
	if err == nil {
		t.Error("expected error")
	}
	c = Clients{&Client{}}
	err = c.Validate(nil)
	if err != errors.ErrInvalidRuleOptions {
		t.Error("expected error for invalid rule options")
	}

	c = Clients{}
	err = c.Validate(nil)
	if err != nil {
		t.Error(err)
	}
}
