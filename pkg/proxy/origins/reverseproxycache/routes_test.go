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

package reverseproxycache

import (
	"testing"

	"github.com/trickstercache/trickster/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/pkg/util/testing"
)

const localResponse = "localresponse"

func TestRegisterHandlers(t *testing.T) {
	c := &Client{}
	c.registerHandlers()
	if _, ok := c.handlers[localResponse]; !ok {
		t.Errorf("expected to find handler named: %s", localResponse)
	}
}

func TestHandlers(t *testing.T) {
	c := &Client{}
	m := c.Handlers()
	if _, ok := m[localResponse]; !ok {
		t.Errorf("expected to find handler named: %s", localResponse)
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	client := &Client{name: "test"}
	ts, _, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "rpc", "/", "debug")
	rsc := request.GetResources(r)
	rsc.OriginClient = client
	client.config = rsc.OriginConfig
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	dpc := client.DefaultPathConfigs(client.config)

	if _, ok := dpc["/-GET-HEAD"]; !ok {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 2
	if len(dpc) != expectedLen {
		t.Errorf("expected ordered length to be: %d got %d", expectedLen, len(dpc))
	}

}
