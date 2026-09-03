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

package druid

import (
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

func TestNewClient(t *testing.T) {
	o := bo.New()
	o.Provider = providers.Druid
	c, err := NewClient("druid-test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "druid-test" {
		t.Fatalf("client name %q", c.Name())
	}
	if !o.FastForwardDisable {
		t.Fatal("Druid must disable Fast Forward")
	}
	if c.(*Client).Modeler() == nil {
		t.Fatal("Druid modeler is nil")
	}
}

func TestUnmarshalInstantaneous(t *testing.T) {
	ts, err := (&Client{}).UnmarshalInstantaneous(nil)
	if err != nil || ts != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", ts, err)
	}
}
