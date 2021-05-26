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
	"testing"

	"github.com/trickstercache/trickster/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/pkg/util/testing"
)

func TestRegisterHandlers(t *testing.T) {
	c := &Client{}
	c.registerHandlers()
	if _, ok := c.handlers["query"]; !ok {
		t.Errorf("expected to find handler named: %s", "query")
	}
}

func TestHandlers(t *testing.T) {
	c := &Client{}
	m := c.Handlers()
	if _, ok := m["query"]; !ok {
		t.Errorf("expected to find handler named: %s", "query")
	}
}

func TestDefaultPathConfigs(t *testing.T) {

	client := &Client{name: "test"}
	ts, _, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 204, "", nil, "clickhouse", "/", "debug")
	rsc := request.GetResources(r)
	client.config = rsc.OriginConfig
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	if _, ok := client.config.Paths["/"]; !ok {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 1
	if len(client.config.Paths) != expectedLen {
		t.Errorf("expected %d got %d", expectedLen, len(client.config.Paths))
	}

}
