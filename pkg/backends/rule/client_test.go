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

package rule

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
)

func TestNewNewClient(t *testing.T) {
	c, err := NewClient("test", bo.New(), nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	if c == nil {
		t.Errorf("expected client named %s", "test")
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
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	c.RegisterHandlers(nil)
	if _, ok := c.Handlers()["rule"]; !ok {
		t.Errorf("expected to find handler named: %s", "rule")
	}
}

func TestValidate(t *testing.T) {

	backendClient, err := NewClient("test", &bo.Options{
		RuleOptions: &options.Options{InputType: "header"}}, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	c := Clients{backendClient.(*Client)}
	err = c.validate(nil)
	if err == nil {
		t.Error("expected error")
	}

	backendClient, err = NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	c = Clients{}
	err = c.validate(nil)
	if err != nil {
		t.Error(err)
	}

}

func TestValidateOptions(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	cl := backends.Backends{"test": backendClient}
	err = ValidateOptions(cl, nil)
	if err != nil {
		t.Error(err)
	}
}
