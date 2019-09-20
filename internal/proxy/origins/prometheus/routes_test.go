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

package prometheus

import (
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

func TestRegisterHandlers(t *testing.T) {
	c := &Client{}
	c.registerHandlers()
	if _, ok := c.handlers[mnQueryRange]; !ok {
		t.Errorf("expected to find handler named: %s", mnQueryRange)
	}
}

func TestHandlers(t *testing.T) {
	c := &Client{}
	m := c.Handlers()
	if _, ok := m[mnQueryRange]; !ok {
		t.Errorf("expected to find handler named: %s", mnQueryRange)
	}
}

func TestDefaultPathConfigs(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-origin-url", "http://127.0.0.1", "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	log.Init()
	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	c := &Client{cache: cache}
	dpc, ordered := c.DefaultPathConfigs()

	if _, ok := dpc["/"]; !ok {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 12
	if len(ordered) != expectedLen {
		t.Errorf("expected ordered length to be: %d got %d", expectedLen, len(ordered))
	}

}
