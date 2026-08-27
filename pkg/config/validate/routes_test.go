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

package validate

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

func TestRoutesRulesAndPools(t *testing.T) {
	t.Parallel()

	c, err := config.Load([]string{"-config", "../../../testdata/test.multiple_backends.conf"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := c.Process(); err != nil {
		t.Fatalf("Process: %v", err)
	}
	clients := make(backends.Backends, len(c.Backends))
	if err := RoutesRulesAndPools(c, clients); err != nil {
		t.Fatalf("RoutesRulesAndPools: %v", err)
	}
	if _, ok := clients["frontend"]; !ok {
		t.Fatal("expected frontend client to be registered")
	}
}

func TestRoutesRulesAndPoolsWithRules(t *testing.T) {
	t.Parallel()

	c, err := config.Load([]string{"-config", "../../../testdata/test.routing.rules.conf"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := c.Process(); err != nil {
		t.Fatalf("Process: %v", err)
	}
	clients := make(backends.Backends, len(c.Backends))
	if err := RoutesRulesAndPools(c, clients); err != nil {
		t.Fatalf("RoutesRulesAndPools: %v", err)
	}
}
